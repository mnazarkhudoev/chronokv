package chronokv

import (
	"bytes"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// cutoff is the oldest timestamp reads are still allowed to ask about.
//
// It's based on max(clock, lastTS) rather than the clock alone, and it never
// goes below the highest cutoff compaction has already applied. Both
// matter when the system clock jumps backwards: without the floor, a read
// could ask about a moment whose versions compaction already threw away and
// get a wrong answer instead of an error.
func (db *DB) cutoff() uint64 {
	floor := db.cutoffFloor.Load()
	if db.opts.Retention <= 0 {
		return floor
	}
	now := max(db.clockNanos(), db.lastTS.Load())
	r := uint64(db.opts.Retention)
	var c uint64
	if now > r {
		c = now - r
	}
	return max(c, floor)
}

// readTS resolves the timestamp a read should use.
func (db *DB) readTS(ro readOpts) (uint64, error) {
	visible := db.visibleTS.Load()
	if !ro.hasAsOf {
		return visible, nil
	}
	if ro.asOf < db.cutoff() {
		return 0, ErrHistoryTruncated
	}
	return min(ro.asOf, visible), nil
}

// Get returns the value of key. With AsOf it returns the value as of that
// timestamp. It returns ErrNotFound if the key didn't exist (or was deleted)
// at that time.
func (db *DB) Get(key []byte, opts ...ReadOption) ([]byte, error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	ts, err := db.readTS(collectReadOpts(opts))
	if err != nil {
		return nil, err
	}

	db.mu.Lock()
	mems := make([]*memtableRef, 0, 1+len(db.imm))
	mems = append(mems, db.mem)
	for i := len(db.imm) - 1; i >= 0; i-- {
		mems = append(mems, db.imm[i])
	}
	db.mu.Unlock()

	seek := keys.SeekKey(key, ts)
	prefix := keys.UserKeyPrefix(key)
	for _, m := range mems {
		it := m.mem.NewIter()
		if it.SeekGE(seek) && bytes.HasPrefix(it.Key(), prefix) {
			return resolve(it.Key(), it.Value())
		}
	}
	return nil, ErrNotFound
}

// resolve turns the newest visible version of a key into Get's result.
func resolve(ik, value []byte) ([]byte, error) {
	if keys.Kind(ik[len(ik)-1]) == keys.KindDelete {
		return nil, ErrNotFound
	}
	return append([]byte{}, value...), nil
}
