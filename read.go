package chronokv

import (
	"bytes"
	"sort"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
	"github.com/mnazarkhudoev/chronokv/internal/manifest"
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

// testHookGet, if set, runs in the middle of Get. Tests use it to land a
// compaction at the worst possible moment.
var testHookGet func()

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

// readState is what a read needs: the memtables (newest first) and a
// referenced Version. Taken together under db.mu so a concurrent flush can't
// make data vanish between the two.
type readState struct {
	mems    []*memtableRef
	version *manifest.Version
}

func (db *DB) acquireReadState() readState {
	db.mu.Lock()
	defer db.mu.Unlock()
	rs := readState{mems: make([]*memtableRef, 0, 1+len(db.imm))}
	rs.mems = append(rs.mems, db.mem)
	for i := len(db.imm) - 1; i >= 0; i-- {
		rs.mems = append(rs.mems, db.imm[i])
	}
	rs.version = db.man.Current()
	return rs
}

// Get returns the value of key. With AsOf it returns the value as of that
// timestamp. It returns ErrNotFound if the key didn't exist (or was deleted)
// at that time.
func (db *DB) Get(key []byte, opts ...ReadOption) ([]byte, error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	// Pin the data first and check the cutoff second. The other order has a
	// gap where a compaction could drop the versions this read needs after
	// it passed the check.
	rs := db.acquireReadState()
	defer rs.version.Unref()
	if testHookGet != nil {
		testHookGet()
	}
	ts, err := db.readTS(collectReadOpts(opts))
	if err != nil {
		return nil, err
	}

	seek := keys.SeekKey(key, ts)
	prefix := keys.UserKeyPrefix(key)
	for _, m := range rs.mems {
		it := m.mem.NewIter()
		if it.SeekGE(seek) && bytes.HasPrefix(it.Key(), prefix) {
			return resolve(it.Key(), it.Value())
		}
	}
	ik, value, found, err := db.getFromVersion(rs.version, seek, prefix)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	return resolve(ik, value)
}

// getFromVersion looks for the newest version <= the seek key's timestamp.
//
// Search order is newest data first: L0 tables newest to oldest (their
// ranges overlap), then one table per deeper level. Within a level >= 1 a
// user key lives in at most one table, and every level holds strictly older
// versions than the one above it, so the first hit is the answer.
func (db *DB) getFromVersion(v *manifest.Version, seek, prefix []byte) (ik, value []byte, found bool, err error) {
	probe := func(f manifest.FileMeta) bool {
		r, e := db.tables.get(f.Num)
		if e != nil {
			err = e
			return true
		}
		if !r.MayContain(prefix) {
			return false
		}
		it := r.NewIter()
		if it.SeekGE(seek) && bytes.HasPrefix(it.Key(), prefix) {
			ik, value, found = it.Key(), it.Value(), true
			return true
		}
		if e := it.Error(); e != nil {
			err = wrapTableErr(tableName(f.Num), e)
			return true
		}
		return false
	}
	inRange := func(f manifest.FileMeta) bool {
		return bytes.Compare(prefix, keys.Prefix(f.Smallest)) >= 0 && bytes.Compare(prefix, keys.Prefix(f.Largest)) <= 0
	}

	for _, f := range v.Levels[0] {
		if inRange(f) && probe(f) {
			return
		}
	}
	for l := 1; l < manifest.NumLevels; l++ {
		files := v.Levels[l]
		i := sort.Search(len(files), func(i int) bool {
			return bytes.Compare(keys.Prefix(files[i].Largest), prefix) >= 0
		})
		if i < len(files) && inRange(files[i]) && probe(files[i]) {
			return
		}
	}
	return
}

// resolve turns the newest visible version of a key into Get's result.
func resolve(ik, value []byte) ([]byte, error) {
	if keys.Kind(ik[len(ik)-1]) == keys.KindDelete {
		return nil, ErrNotFound
	}
	return append([]byte{}, value...), nil
}
