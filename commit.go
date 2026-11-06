package chronokv

import (
	"time"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// writer is one Commit call waiting in the pipeline.
type writer struct {
	batch *Batch
	ts    uint64
	err   error
	done  bool
}

// maxGroupBytes bounds how much one leader writes on behalf of others, so a
// writer at the back of a long queue isn't stuck behind an unbounded group.
const maxGroupBytes = 1 << 20

// NewBatch returns an empty batch.
func (db *DB) NewBatch() *Batch { return &Batch{} }

// Put writes key=value and returns the commit timestamp.
func (db *DB) Put(key, value []byte) (Timestamp, error) {
	b := Batch{}
	b.Put(key, value)
	return db.Commit(&b)
}

// Delete deletes key and returns the commit timestamp. Deleting a key that
// doesn't exist still records a tombstone.
func (db *DB) Delete(key []byte) (Timestamp, error) {
	b := Batch{}
	b.Delete(key)
	return db.Commit(&b)
}

// Now returns a timestamp T such that every commit that has returned is
// visible at T and every later commit gets a timestamp > T. Reading with
// AsOf(Now()) later on gives a stable snapshot of this moment.
func (db *DB) Now() Timestamp {
	ts, _ := db.Commit(&Batch{})
	return ts
}

// Commit applies b atomically and returns its timestamp. An empty batch
// writes nothing and returns the same kind of timestamp as Now.
//
// Commits go through a group commit pipeline: concurrent callers queue up,
// the first in line writes everyone's batches to the WAL with a single
// fsync, applies them to the memtable in order, and then wakes the others.
func (db *DB) Commit(b *Batch) (Timestamp, error) {
	if db.closed.Load() {
		return 0, ErrClosed
	}
	if b.size > maxBatchSize {
		return 0, ErrBatchTooLarge
	}
	w := &writer{batch: b}

	db.commitMu.Lock()
	db.queue = append(db.queue, w)
	for !w.done && db.queue[0] != w {
		db.commitCond.Wait()
	}
	if w.done {
		db.commitMu.Unlock()
		return Timestamp(w.ts), w.err
	}

	// We're the leader. Take a group of writers from the front of the queue.
	n, bytes := 1, b.size
	for n < len(db.queue) && bytes+db.queue[n].batch.size <= maxGroupBytes {
		bytes += db.queue[n].batch.size
		n++
	}
	group := db.queue[:n:n]
	db.commitMu.Unlock()

	err := db.writeGroup(group)

	db.commitMu.Lock()
	for _, g := range group {
		if g.err == nil {
			g.err = err
		}
		g.done = true
	}
	db.queue = db.queue[n:]
	db.commitCond.Broadcast()
	db.commitMu.Unlock()
	return Timestamp(w.ts), w.err
}

func (db *DB) clockNanos() uint64 {
	n := db.opts.Clock().UnixNano()
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// writeGroup does the leader's work: assign timestamps, log, apply, publish.
func (db *DB) writeGroup(group []*writer) error {
	if db.closed.Load() {
		return ErrClosed
	}
	if err := db.getBgErr(); err != nil {
		return err
	}

	db.walMu.Lock()
	lastTS := db.lastTS.Load()
	wrote := false
	for _, g := range group {
		now := db.clockNanos()
		if g.batch.Len() == 0 {
			// Now(): hand out a timestamp but log nothing.
			g.ts = max(now, lastTS)
			lastTS = g.ts
			continue
		}
		g.ts = max(now, lastTS+1)
		lastTS = g.ts
		db.lastSeq++
		db.encBuf = encodeBatch(db.encBuf[:0], g.ts, g.batch)
		if err := db.wal.Append(db.lastSeq, db.encBuf); err != nil {
			db.walMu.Unlock()
			db.setBgErr(err)
			return err
		}
		wrote = true
	}
	var err error
	if wrote {
		if db.opts.SyncMode == SyncAlways {
			err = db.wal.Sync()
		} else {
			err = db.wal.Flush()
		}
	}
	db.walMu.Unlock()
	if err != nil {
		db.setBgErr(err)
		return err
	}
	db.lastTS.Store(lastTS)

	// Apply in batch order. Readers can already see these memtable entries,
	// but they don't look past visibleTS, which only moves once the whole
	// group is in. That's what makes batches atomic for readers.
	db.mu.Lock()
	mem := db.mem.mem
	db.mu.Unlock()
	for _, g := range group {
		for _, op := range g.batch.ops {
			db.ikBuf = keys.Encode(db.ikBuf[:0], op.key, g.ts, op.kind)
			mem.Add(db.ikBuf, op.value)
		}
	}
	db.visibleTS.Store(lastTS)
	return nil
}

// syncLoop fsyncs the WAL periodically in SyncInterval mode.
func (db *DB) syncLoop() {
	defer db.wg.Done()
	t := time.NewTicker(db.opts.SyncEvery)
	defer t.Stop()
	for {
		select {
		case <-db.stop:
			return
		case <-t.C:
			db.walMu.Lock()
			err := db.wal.Sync()
			db.walMu.Unlock()
			if err != nil {
				db.setBgErr(err)
			}
		}
	}
}
