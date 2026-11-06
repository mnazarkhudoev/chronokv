package chronokv

import (
	"bufio"
	"os"
	"path/filepath"

	"github.com/mnazarkhudoev/chronokv/internal/manifest"
	"github.com/mnazarkhudoev/chronokv/internal/memtable"
	"github.com/mnazarkhudoev/chronokv/internal/sstable"
	"github.com/mnazarkhudoev/chronokv/internal/wal"
)

// Flush writes everything currently in memory to SSTables and waits for it
// to finish. It's not needed for durability (the WAL covers that); it's
// useful for tests, tools, and taking checkpoints.
func (db *DB) Flush() error {
	db.userOps.RLock()
	defer db.userOps.RUnlock()
	return db.flush()
}

func (db *DB) flush() error {
	if db.closed.Load() {
		return ErrClosed
	}
	// Rotation has to happen inside the commit pipeline so it can't race
	// with a leader writing to the memtable.
	w := &writer{batch: &Batch{}, rotate: true}
	if _, err := db.commit(w); err != nil {
		return err
	}
	if err := db.flushPending(); err != nil {
		return err
	}
	return db.getBgErr()
}

// rotate turns the active memtable into an immutable one and starts a new
// WAL segment for its successor. Called only by the commit leader. If too
// many memtables are already waiting for a flush, it blocks: that is the
// write stall that keeps memory bounded when disks can't keep up.
func (db *DB) rotate() error {
	db.mu.Lock()
	for len(db.imm) >= db.opts.MaxImmutableMemtables && db.bgErr == nil {
		db.flushCond.Wait()
	}
	err := db.bgErr
	db.mu.Unlock()
	if err != nil {
		return err
	}

	num := db.man.NewFileNum()
	w, err := wal.Create(filepath.Join(db.dir, wal.SegmentName(num)))
	if err != nil {
		db.setBgErr(err)
		return err
	}
	db.walMu.Lock()
	old := db.wal
	db.wal, db.walNum = w, num
	db.walMu.Unlock()
	if err := old.Close(); err != nil {
		db.setBgErr(err)
		return err
	}

	db.mu.Lock()
	db.imm = append(db.imm, db.mem)
	db.mem = &memtableRef{mem: memtable.New(), logNum: num}
	db.mu.Unlock()

	select {
	case db.flushCh <- struct{}{}:
	default:
	}
	return nil
}

func (db *DB) flushLoop() {
	defer db.wg.Done()
	for {
		select {
		case <-db.stop:
			return
		case <-db.flushCh:
			db.flushPending()
		}
	}
}

// flushPending flushes immutable memtables, oldest first, until none are
// left.
func (db *DB) flushPending() error {
	db.flushMu.Lock()
	defer db.flushMu.Unlock()
	for {
		db.mu.Lock()
		if len(db.imm) == 0 || db.bgErr != nil {
			err := db.bgErr
			db.mu.Unlock()
			return err
		}
		ref := db.imm[0]
		nextLog := db.mem.logNum
		if len(db.imm) > 1 {
			nextLog = db.imm[1].logNum
		}
		db.mu.Unlock()

		if err := db.flushOne(ref.mem, nextLog); err != nil {
			db.setBgErr(err)
			db.mu.Lock()
			db.flushCond.Broadcast()
			db.mu.Unlock()
			return err
		}
	}
}

func (db *DB) flushOne(mem *memtable.Memtable, nextLog uint64) error {
	db.walMu.Lock()
	lastSeq := db.lastSeq
	db.walMu.Unlock()
	edit := manifest.Edit{LogNum: nextLog, LastSeq: lastSeq, LastTS: db.lastTS.Load()}

	var num uint64
	if !mem.Empty() {
		num = db.man.NewFileNum()
		db.addPending(num)
		defer db.removePending(num)
		meta, err := db.writeTable(num, mem.NewIter())
		if err != nil {
			return err
		}
		edit.Added = []manifest.NewFile{{Level: 0, Meta: meta}}
	}
	// The table must be in the manifest before the memtable disappears, so
	// a reader always finds the data in one place or the other.
	if err := db.man.Apply(edit); err != nil {
		return err
	}
	db.mu.Lock()
	db.imm = db.imm[1:]
	db.flushCond.Broadcast()
	db.mu.Unlock()

	db.deleteObsoleteFiles(false)
	db.maybeScheduleCompaction()
	return nil
}

// tableIter is the subset of an iterator writeTable needs.
type tableIter interface {
	First() bool
	Next() bool
	Key() []byte
	Value() []byte
	Error() error
}

// tableBuilder writes one table via a temp file that is renamed into
// place once it is complete and synced, so a crash never leaves a
// half-written .sst behind (only a .tmp, which Open cleans up).
type tableBuilder struct {
	num        uint64
	final, tmp string
	f          *os.File
	bw         *bufio.Writer
	tw         *sstable.Writer
}

func (db *DB) newTableBuilder(num uint64) (*tableBuilder, error) {
	final := filepath.Join(db.dir, tableName(num))
	b := &tableBuilder{num: num, final: final, tmp: final + ".tmp"}
	f, err := os.Create(b.tmp)
	if err != nil {
		return nil, err
	}
	b.f = f
	b.bw = bufio.NewWriterSize(f, 256<<10)
	b.tw = sstable.NewWriter(b.bw, sstable.WriterOptions{BlockSize: db.opts.BlockSize, BloomBitsPerKey: db.opts.BloomBitsPerKey})
	return b, nil
}

func (b *tableBuilder) add(ik, value []byte) error { return b.tw.Add(ik, value) }
func (b *tableBuilder) estimatedSize() int64       { return b.tw.EstimatedSize() }

func (b *tableBuilder) abort() {
	b.f.Close()
	os.Remove(b.tmp)
}

func (b *tableBuilder) finish() (manifest.FileMeta, error) {
	props, err := b.tw.Finish()
	if err == nil {
		err = b.bw.Flush()
	}
	if err == nil {
		err = b.f.Sync()
	}
	if err != nil {
		b.abort()
		return manifest.FileMeta{}, err
	}
	if err := b.f.Close(); err != nil {
		os.Remove(b.tmp)
		return manifest.FileMeta{}, err
	}
	if err := os.Rename(b.tmp, b.final); err != nil {
		os.Remove(b.tmp)
		return manifest.FileMeta{}, err
	}
	if err := wal.SyncDir(filepath.Dir(b.final)); err != nil {
		return manifest.FileMeta{}, err
	}
	return manifest.FileMeta{Num: b.num, Size: props.Size, Smallest: props.Smallest, Largest: props.Largest}, nil
}

// writeTable writes every entry of it into table num.
func (db *DB) writeTable(num uint64, it tableIter) (manifest.FileMeta, error) {
	b, err := db.newTableBuilder(num)
	if err != nil {
		return manifest.FileMeta{}, err
	}
	for ok := it.First(); ok; ok = it.Next() {
		if err := b.add(it.Key(), it.Value()); err != nil {
			b.abort()
			return manifest.FileMeta{}, err
		}
	}
	if err := it.Error(); err != nil {
		b.abort()
		return manifest.FileMeta{}, err
	}
	return b.finish()
}
