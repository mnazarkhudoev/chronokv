# chronokv

[![ci](https://github.com/mnazarkhudoev/chronokv/actions/workflows/ci.yml/badge.svg)](https://github.com/mnazarkhudoev/chronokv/actions/workflows/ci.yml)

A key-value store in Go that remembers. Every write is versioned, so besides
the usual get/put/delete you can ask what a key (or a whole range of keys)
looked like at any point in the past, as far back as the retention window
you configure.

```
$ chronokv ./data
chronokv shell, type "help" for commands
chronokv> put user:42 {"plan":"free"}
ok @ 2026-11-06T23:12:21.992449578Z (1794006741992449578)
chronokv> put user:42 {"plan":"pro"}
ok @ 2026-11-06T23:12:24.997322395Z (1794006744997322395)
chronokv> get user:42
"{\"plan\":\"pro\"}"
chronokv> get user:42 asof -2s
"{\"plan\":\"free\"}"
chronokv> del user:42
ok @ 2026-11-06T23:12:24.997486641Z (1794006744997486641)
chronokv> get user:42
(nil)
chronokv> history user:42
2026-11-06T23:12:24.997486641Z (1794006744997486641)  (deleted)
2026-11-06T23:12:24.997322395Z (1794006744997322395)  "{\"plan\":\"pro\"}"
2026-11-06T23:12:21.992449578Z (1794006741992449578)  "{\"plan\":\"free\"}"
```

It's a from-scratch LSM tree: write-ahead log, skiplist memtable, SSTables
with bloom filters, leveled compaction, crash recovery. It only uses the Go
standard library.

## Why I built this

I wanted to actually understand how storage engines like LevelDB, RocksDB
and Pebble work, and reading about LSM trees only gets you so far. I also
like the question "what did this record look like yesterday, before that
bad deploy?" Most databases can't answer it without digging through logs
or restoring a backup.

It turns out these two things fit together nicely. If you put the
timestamp *into the key* (with the bits flipped so newer versions sort
first), an LSM tree gives you time travel almost for free: "value of k at
time T" is just a seek to `(k, T)`. The hard parts are elsewhere:
deciding which old versions are safe to throw away, and making sure the
answer is never silently wrong when the clock misbehaves. That's most of
what this project is about.

## Using it

As a library:

```go
db, err := chronokv.Open("./data", chronokv.Options{Retention: 24 * time.Hour})
if err != nil {
	log.Fatal(err)
}
defer db.Close()

before, _ := db.Put([]byte("user:42"), []byte(`{"plan":"free"}`))
db.Put([]byte("user:42"), []byte(`{"plan":"pro"}`))

now, _ := db.Get([]byte("user:42"))                          // {"plan":"pro"}
then, _ := db.Get([]byte("user:42"), chronokv.AsOf(before))  // {"plan":"free"}

// a consistent snapshot of a range, as of some moment
it := db.Scan([]byte("user:"), []byte("user;"), chronokv.AsOf(before))
defer it.Close()
for it.Next() {
	fmt.Printf("%s = %s\n", it.Key(), it.Value())
}

// every version of a key, newest first
history, _ := db.History([]byte("user:42"), 0)
```

These snippets are also in `example_test.go`, so `go test` checks they work.

A few things worth knowing:

- `Put`, `Delete` and `Commit` return the timestamp the write got. Save it
  if you want to read "as of right after this write" later.
- `db.Now()` gives you a timestamp for "this moment" that is stable: any
  write after it gets a later timestamp.
- Batches (`db.NewBatch()`, `db.Commit(b)`) are atomic. All writes in a
  batch share one timestamp, so no reader at any point in time sees half a
  batch.
- Reading older than the retention window returns `ErrHistoryTruncated`.
  It doesn't guess.

The shell:

```
go install github.com/mnazarkhudoev/chronokv/cmd/chronokv@latest
chronokv -retention 24h ./data
```

`asof` takes a raw timestamp, a negative duration (`-5m`, `-1h30m`), an
RFC 3339 time, or `now`. Type `help` for the rest.

## How it works

```
 Put ──► WAL (fsync, group commit) ──► memtable (skiplist)
                                           │ full
                                           ▼
                                      L0 sstables ──► compaction ──► L1 … L6
```

The internal key is `escape(userKey) | 00 00 | ^timestamp | kind`. Plain
byte comparison then sorts by user key and puts the newest version first,
so nothing in the engine needs a custom comparator. Zero bytes in user keys
are escaped, which is what keeps keys like `a` and `a\x00` in the right
order.

Compaction drops old versions using the retention window, with one catch.
For every key it keeps the newest version *older* than the cutoff, because
that's the correct answer for reads between the cutoff and the next write.
The cutoff can never move backwards, even if the system clock does. The
highest cutoff ever used is saved in the manifest, and reads below it fail
with `ErrHistoryTruncated` instead of returning data that might already be
gone.

[docs/design.md](docs/design.md) has the full write-up: the byte layout,
group commit, why reads use a "visible timestamp", recovery rules, and the
backward-clock problem in detail.

## Testing

This is the part I spent the most time on, because a storage engine that's
wrong in rare cases is worse than useless.

- **Model-based test.** Random sequences of puts, deletes, batches,
  flushes, compactions, reopens and clock jumps (including backwards) run
  against both the database and a deliberately dumb in-memory model that
  keeps every version forever. After every step both get the same questions
  at random points in time and have to agree. On its very first run it
  found a real bug: writing the same key twice in one batch produced two
  versions with the same timestamp.
- **Crash test.** A child process writes as fast as it can and gets killed
  with `SIGKILL` at a random moment. The test reopens the database and
  checks that every acknowledged write survived, that nothing is out of
  order, and that batches are all-or-nothing. 200 rounds in the long mode.
- **Fuzzing** of the key encoding, the WAL reader and the SSTable reader.
- Everything runs under `-race`.

```
make test          # go test -race -short ./...
make test-long     # longer model + crash runs
make fuzz          # each fuzz target for 30s

# replay a failing model test seed
go test -run TestModel . -args -seed=1794006353566407029
```

## Status

The engine is done and tested. Next on my list:

- [ ] a server speaking the Redis protocol, so `redis-cli` works, with
      `GET key ASOF -5m`, `HISTORY key` and `NOW` commands
- [ ] leader/follower replication by streaming the WAL (followers apply
      the leader's timestamps, so `ASOF` reads agree across nodes)
- [ ] benchmarks against bbolt and Pebble

## Limitations

Being honest about what it isn't:

- No block cache and no compression yet. The OS page cache is doing a lot
  of work.
- One compaction at a time, so heavy write loads will stall writers
  sooner than a production engine would.
- No transactions beyond atomic batches.
- Developed and tested on Linux. It builds elsewhere, but the directory
  lock (`flock`) and the crash test are unix only.
- It's a learning project that I've tested hard, not something I'd put
  under production data yet.

## License

MIT
