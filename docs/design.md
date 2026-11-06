# chronokv design notes

These are my notes on how the engine works and why it's built the way it
is. The README has the short version; this goes into the details I'd want
to know if I were reading someone else's storage engine.

## The one idea everything hangs on

Every write is stored under an *internal key* that includes its timestamp:

```
escape(userKey) | 00 00 | bigEndian(^ts) | kind
```

- `escape` turns every `00` byte in the user key into `00 FF`. After that
  the user key can't contain `00 00`, so the first `00 00` is always the
  separator.
- `^ts` is the timestamp with all bits flipped. Bigger timestamps become
  smaller numbers, so the newest version of a key sorts first.
- `kind` is 1 for a value and 0 for a tombstone (deletion).

A worked example, the key `"a\x00"` written at ts=5:

```
61 00 FF | 00 00 | FF FF FF FF FF FF FF FA | 01
 a  \0     sep     ^5                         set
```

With this layout plain `bytes.Compare` gives exactly the order I need:
user keys ascending, and inside one user key the newest version first. So
there's no custom comparator anywhere: the skiplist, SSTables, the merging
iterator and compaction all just compare bytes.

The escaping matters more than it looks. Without it, `"a"` and `"a\x00"`
would produce internal keys where the separator of one looks like part of
the other, and range scans with bounds like `["a\x00", "ab")` return wrong
results. The fuzz test `FuzzOrdering` in `internal/keys` checks that the
internal key order always matches (user key, newest first), for arbitrary
bytes.

### Time travel is just a seek

"What was `k` at time T?" becomes: seek to `(k, T)` and take the first entry
at or after it. If that entry still belongs to `k`, it's the newest
version with a timestamp ≤ T. A value means that's the answer; a tombstone
means the key didn't exist at T. If the seek lands on a different key,
`k` didn't exist yet at T.

Scans work the same way, per user key: `internal/iterator.Visible` walks a
merged stream of internal keys, and for each user key it picks the first
version ≤ T. When it sees versions newer than T it doesn't step through
them one at a time; it seeks straight to `(k, T)`, so a key with a long
history after T costs one seek.

## Write path

```
Put/Delete/Commit
      │
      ▼
 commit queue ──► leader takes a group of batches (up to 1 MiB)
                    │ assign ts = max(clock, last+1) per batch
                    │ append each batch to the WAL
                    │ one fsync for the whole group
                    │ insert into the memtable, in batch order
                    │ publish visibleTS
                    ▼
               wake up the others
```

- **Timestamps** come from the database, not the caller:
  `max(wallclock, lastTS+1)`. They strictly increase even if the system
  clock jumps back, so two writes never share a timestamp and order is
  never ambiguous.
- **Batches** share one timestamp. That's what makes them atomic for
  readers who use `AsOf`: at any T you see the whole batch or none of it.
  Writing the same key twice in one batch keeps the last write. (Two
  versions of one key with the same timestamp would be ambiguous. The
  model test found this bug for me; see Testing below.)
- **Group commit**: with `SyncAlways` each commit waits for an fsync, but
  concurrent commits share it. The first writer in the queue does the I/O
  for everyone queued behind it.
- **visibleTS** fixes a subtle problem. The memtable entries of a batch
  are inserted one by one, and readers can see them as soon as they're
  linked in. Reads without `AsOf` don't use "infinity" as their
  timestamp; they use `visibleTS`, which only moves forward once the whole
  group is in the memtable. `TestBatchAtomicVisibility` writes `y` before
  `x` in each batch and checks that readers never see the new `y` with the
  old `x`. I confirmed it fails if reads use the max timestamp instead.

When the memtable reaches `MemtableSize` (4 MiB by default) it becomes
immutable, a new WAL segment starts, and a background goroutine flushes it
to an L0 SSTable. If flushing falls behind (more than
`MaxImmutableMemtables` waiting), writers stall instead of using
unbounded memory.

## Read path

`Get(k, AsOf(T))` checks, in order:

1. the active memtable,
2. immutable memtables, newest first,
3. L0 tables, newest first (their key ranges overlap),
4. L1 through L6, one table per level (binary search on key ranges).

The first version ≤ T it finds wins. That's only correct because every
layer holds strictly newer versions than the layers below it, and the
engine has to keep that true: flushes produce new L0 tables, and L0→L1
compaction takes *all* of L0 at once.

SSTables have a bloom filter keyed on the user-key part of internal keys,
so one filter entry covers every version of a key, and a "no" skips the
table for any T.

Readers pin a `Version` (the set of tables at the moment the read
started). Compaction can replace tables in newer versions, but files are
only deleted when no pinned version references them. An open `Scan` keeps
its files alive until `Close`.

## Retention

Keeping every version forever is an option (`Retention: 0`), but normally
you want a window, like "24 hours of history". Compaction applies it like
this, for each user key (input is newest first):

- keep every version with `ts >= cutoff`;
- keep **one** version older than the cutoff, the newest one;
- drop everything older than that.

The one older version has to stay because it's the answer to a read
anywhere between the cutoff and the next version. Say the window is 1h, a
key was set to `A` 3 hours ago and to `B` 10 minutes ago. A read "as of 30
minutes ago" is inside the window, and its answer is `A`, which is older
than the cutoff.

If that last old version is a tombstone, and nothing older can exist in
deeper levels ("bottommost" compaction), the tombstone can go too. A read
would see "not found" either way.

Reads older than the cutoff return `ErrHistoryTruncated`. chronokv never
guesses: if the answer might have been compacted away, you get an error,
not stale data.

### The cutoff can only move forward

This is the subtle part. The cutoff is roughly `now - Retention`. If the
system clock jumps backwards (NTP correction, VM migration, someone
setting the date by hand), `now - Retention` jumps backwards too. Reads
would then be allowed again at times whose versions a compaction already
dropped, and they'd get wrong answers instead of an error.

So the read cutoff is:

```
cutoff = max(cutoffFloor, max(clock, lastTS) - Retention)
```

where `cutoffFloor` is the largest cutoff any compaction has ever used. It
is saved in the manifest, so it survives restarts.
`TestBackwardClockAfterCompaction` covers this. Writing it showed me
something I didn't expect: `Flush` goes through the commit pipeline and
bumps `lastTS`, which hid the bug in my first version of the test. The
final version compacts the way the background goroutine does, and I
checked that it fails without the floor.

## Files on disk

```
data/
  CURRENT            name of the active manifest
  MANIFEST-000012    log of version edits (which tables, which level, counters)
  000013.wal         write-ahead log segment(s)
  000010.sst         sorted tables
  LOCK               flock'd while the database is open
```

- **WAL records**: `len u32 | crc32c u32 | seq u64 | payload`. The payload is
  the encoded batch including its timestamp, so replay reproduces exactly
  the same versions. (Replication will reuse this: followers apply the
  leader's records with the leader's timestamps.)
- **SSTables**: data blocks (4 KiB, prefix compressed, restart points every
  16 entries), a bloom filter, an index block, and a fixed 44-byte footer.
  Every block has its own CRC.
- **Manifest**: edits framed exactly like WAL records. On every open it
  is rewritten as a single snapshot edit and `CURRENT` is swapped
  atomically (write temp file, fsync, rename, fsync dir), so it never grows
  without bound.
- New tables are written as `NNNNNN.sst.tmp` and renamed into place after
  an fsync. A crash can leave a `.tmp` behind, and `Open` deletes it.

## Recovery

On `Open`:

1. read `CURRENT`, replay the manifest (a torn last edit is ignored: it
   was never acknowledged),
2. replay every WAL segment numbered ≥ `LogNum` into a fresh memtable,
3. delete orphans: tables no version references, WAL segments below
   `LogNum`, leftover `.tmp` files.

For the WAL, a bad record at the **end of the newest segment** is what a
crash mid-write looks like. That write never returned to its caller, so
the segment is truncated there. A bad record anywhere else is real damage,
and `Open` fails with `ErrCorrupted` instead of quietly losing data.

## Compaction

Leveled, one compaction at a time, in a background goroutine:

- L0 → L1 when L0 has 4 or more tables (all of L0 plus overlapping L1).
- Ln → Ln+1 when a level is over budget (10 MiB for L1, ×10 per level):
  one table, picked round-robin across the key space, plus the tables it
  overlaps in the next level.
- Output tables are split around 2 MiB, but only between user keys. In L1
  and below, all versions of a key are in one table, so point reads only
  ever look at one table per level.

`internal/compaction` doesn't do any I/O. `Pick` looks at a `Version` and
returns which tables to merge; `Merge` takes an iterator and applies the
retention rule. That split made both easy to unit test.

## Testing

The tests I trust most:

- **Model test** (`model_test.go`): random puts, deletes, batches (with
  repeated keys), flushes, compactions, reopens and clock jumps (forward
  *and* backward), against a dumb in-memory oracle that keeps every
  version forever. After each step it asks both the same `Get`/`Scan`/
  `History` questions at random times inside the window, and checks that
  reads below the cutoff fail. Seeds are random and printed on failure;
  `go test -run TestModel . -args -seed=N` replays one. Its first run
  found the same-key-twice-in-a-batch bug.
- **Crash test** (`crash_test.go`): a child process writes as fast as it
  can and gets `SIGKILL`ed at a random moment. Then the parent reopens the
  database and checks that every acknowledged write is there, readable at
  its own timestamp, that the data is a prefix of what was written, and
  that batches are all-or-nothing. It reuses the same directory across
  iterations, so it also recovers from earlier recoveries.
- **Fuzzing**: key encoding order, the WAL reader and the SSTable reader on
  garbage input (must never panic).

## Things that are deliberately missing (for now)

- No block cache. Every block read is a `ReadAt`, so the OS page cache does
  the work.
- No compression.
- One compaction at a time.
- No transactions beyond atomic batches (no read-modify-write isolation).
- `Now()` raises the timestamp counter in memory but doesn't log it. If the
  clock is set back *and* the process restarts, a new write could get a
  timestamp ≤ a `Now()` value handed out before the restart.
