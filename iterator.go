package chronokv

import (
	"bytes"
	"sort"
	"sync"

	"github.com/mnazarkhudoev/chronokv/internal/iterator"
	"github.com/mnazarkhudoev/chronokv/internal/manifest"
	"github.com/mnazarkhudoev/chronokv/internal/sstable"
)

// Iterator walks a consistent snapshot of a key range. Always Close it: an
// open iterator keeps the files it reads from alive.
type Iterator struct {
	vis     *iterator.Visible
	version *manifest.Version
	err     error
	once    sync.Once
}

// Scan returns an iterator over keys in [start, end) in ascending order. A
// nil start or end means unbounded. With AsOf, the iterator shows the range
// as it was at that time; without it, as of the moment Scan was called.
// Writes made after Scan returns are never visible to it.
func (db *DB) Scan(start, end []byte, opts ...ReadOption) *Iterator {
	if db.closed.Load() {
		return &Iterator{err: ErrClosed}
	}
	rs := db.acquireReadState() // before the cutoff check, see Get
	ts, err := db.readTS(collectReadOpts(opts))
	if err != nil {
		rs.version.Unref()
		return &Iterator{err: err}
	}
	return &Iterator{
		vis:     iterator.NewVisible(db.newInternalIter(rs), ts, start, end),
		version: rs.version,
	}
}

// Next advances to the next key. It returns false at the end or on error;
// check Err afterwards.
func (it *Iterator) Next() bool {
	if it.err != nil || it.vis == nil {
		return false
	}
	if it.vis.Next() {
		return true
	}
	if err := it.vis.Error(); err != nil {
		it.err = wrapTableErr("scan", err)
	}
	return false
}

// Key returns the current key. It is only valid until the next call to Next.
func (it *Iterator) Key() []byte { return it.vis.Key() }

// Value returns the current value. It is only valid until the next call to
// Next.
func (it *Iterator) Value() []byte { return it.vis.Value() }

// Err returns the error that stopped iteration, if any.
func (it *Iterator) Err() error { return it.err }

// Close releases the iterator. It is safe to call more than once.
func (it *Iterator) Close() error {
	it.once.Do(func() {
		if it.vis != nil {
			it.vis.Close()
		}
		if it.version != nil {
			it.version.Unref()
		}
	})
	return nil
}

// newInternalIter merges every source in rs into one stream of internal
// keys: memtables newest first, then L0 tables newest first, then one
// lazily-opened iterator per deeper level.
func (db *DB) newInternalIter(rs readState) iterator.Iterator {
	var its []iterator.Iterator
	for _, m := range rs.mems {
		its = append(its, m.mem.NewIter())
	}
	for _, f := range rs.version.Levels[0] {
		its = append(its, &levelIter{db: db, files: []manifest.FileMeta{f}})
	}
	for l := 1; l < manifest.NumLevels; l++ {
		if len(rs.version.Levels[l]) > 0 {
			its = append(its, &levelIter{db: db, files: rs.version.Levels[l]})
		}
	}
	return iterator.NewMerging(its...)
}

// levelIter iterates over a sorted run of non-overlapping tables, opening
// each one only when the iteration actually gets there.
type levelIter struct {
	db    *DB
	files []manifest.FileMeta
	idx   int
	cur   *sstable.Iter
	err   error
}

func (l *levelIter) open(i int) bool {
	l.idx = i
	l.cur = nil
	if i >= len(l.files) {
		return false
	}
	r, err := l.db.tables.get(l.files[i].Num)
	if err != nil {
		l.err = err
		return false
	}
	l.cur = r.NewIter()
	return true
}

// settle moves to the following tables while the current one is exhausted.
func (l *levelIter) settle() bool {
	for l.cur != nil && !l.cur.Valid() {
		if err := l.cur.Error(); err != nil {
			l.err = wrapTableErr(tableName(l.files[l.idx].Num), err)
			l.cur = nil
			return false
		}
		if !l.open(l.idx + 1) {
			return false
		}
		l.cur.First()
	}
	return l.cur != nil
}

func (l *levelIter) First() bool {
	if l.err != nil || !l.open(0) {
		return false
	}
	l.cur.First()
	return l.settle()
}

func (l *levelIter) SeekGE(key []byte) bool {
	if l.err != nil {
		return false
	}
	i := sort.Search(len(l.files), func(i int) bool { return bytes.Compare(l.files[i].Largest, key) >= 0 })
	if !l.open(i) {
		return false
	}
	l.cur.SeekGE(key)
	return l.settle()
}

func (l *levelIter) Next() bool {
	if l.err != nil || l.cur == nil {
		return false
	}
	l.cur.Next()
	return l.settle()
}

func (l *levelIter) Valid() bool   { return l.err == nil && l.cur != nil && l.cur.Valid() }
func (l *levelIter) Key() []byte   { return l.cur.Key() }
func (l *levelIter) Value() []byte { return l.cur.Value() }
func (l *levelIter) Error() error  { return l.err }
func (l *levelIter) Close() error  { l.cur = nil; return nil }
