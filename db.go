package chronokv

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/mnazarkhudoev/chronokv/internal/compaction"
	"github.com/mnazarkhudoev/chronokv/internal/keys"
	"github.com/mnazarkhudoev/chronokv/internal/manifest"
	"github.com/mnazarkhudoev/chronokv/internal/memtable"
	"github.com/mnazarkhudoev/chronokv/internal/wal"
)

// memtableRef is a memtable plus the oldest WAL segment it depends on. Once
// the memtable is flushed, segments before the next memtable's logNum can go.
type memtableRef struct {
	mem    *memtable.Memtable
	logNum uint64
}

// DB is a chronokv database. It is safe for concurrent use.
type DB struct {
	dir  string
	opts Options
	log  *slog.Logger
	lock io.Closer
	man  *manifest.Manifest

	// Commit pipeline. Writers queue up under commitMu; the writer at the
	// head of the queue is the leader and does the I/O for a whole group.
	commitMu   sync.Mutex
	commitCond *sync.Cond
	queue      []*writer

	// walMu guards the WAL writer (the leader appends, the background
	// syncer fsyncs). lastSeq and encBuf are only touched by the leader.
	walMu   sync.Mutex
	wal     *wal.Writer
	walNum  uint64
	lastSeq uint64
	encBuf  []byte
	ikBuf   []byte

	lastTS      atomic.Uint64 // highest timestamp handed out
	visibleTS   atomic.Uint64 // highest timestamp whose batch is fully applied
	cutoffFloor atomic.Uint64 // highest retention cutoff compaction has used

	mu        sync.Mutex // guards the fields below
	mem       *memtableRef
	imm       []*memtableRef // oldest first
	bgErr     error
	pending   map[uint64]struct{} // table numbers being written
	flushCond *sync.Cond          // signalled when imm shrinks or bgErr is set

	tables     *tableCache
	flushCh    chan struct{}
	flushMu    sync.Mutex // one flush at a time
	obsoleteMu sync.Mutex

	compactCh     chan struct{}
	compactMu     sync.Mutex // one compaction at a time
	compactOpts   compaction.Options
	compactCursor [manifest.NumLevels][]byte

	closed atomic.Bool
	stop   chan struct{}
	wg     sync.WaitGroup
}

// Open opens (or creates) the database in dir.
func Open(dir string, opts Options) (*DB, error) {
	opts = opts.withDefaults()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lock, err := lockDir(dir)
	if err != nil {
		return nil, err
	}
	db := &DB{
		dir: dir, opts: opts, log: opts.Logger, lock: lock,
		stop:      make(chan struct{}),
		pending:   map[uint64]struct{}{},
		tables:    newTableCache(dir),
		flushCh:   make(chan struct{}, 1),
		compactCh: make(chan struct{}, 1),

		compactOpts: opts.compaction,
	}
	db.commitCond = sync.NewCond(&db.commitMu)
	db.flushCond = sync.NewCond(&db.mu)
	if err := db.open(); err != nil {
		if db.man != nil {
			db.man.Close()
		}
		lock.Close()
		return nil, err
	}
	db.man.SetReleaseHook(func() { db.deleteObsoleteFiles(false) })
	db.deleteObsoleteFiles(true)
	db.wg.Add(2)
	go db.flushLoop()
	go db.compactLoop()
	db.maybeScheduleCompaction()
	if opts.SyncMode == SyncInterval {
		db.wg.Add(1)
		go db.syncLoop()
	}
	return db, nil
}

func (db *DB) open() error {
	var err error
	if _, statErr := os.Stat(filepath.Join(db.dir, "CURRENT")); statErr == nil {
		db.man, err = manifest.Load(db.dir)
	} else if errors.Is(statErr, fs.ErrNotExist) {
		db.man, err = manifest.Create(db.dir)
	} else {
		err = statErr
	}
	if err != nil {
		return err
	}
	state := db.man.State()
	db.lastSeq = state.LastSeq
	db.lastTS.Store(state.LastTS)
	db.cutoffFloor.Store(state.Cutoff)

	mem := memtable.New()
	firstLog, err := db.replayWAL(state.LogNum, mem)
	if err != nil {
		return err
	}
	db.walNum = db.man.NewFileNum()
	if db.wal, err = wal.Create(filepath.Join(db.dir, wal.SegmentName(db.walNum))); err != nil {
		return err
	}
	if firstLog == 0 {
		firstLog = db.walNum
	}
	db.mem = &memtableRef{mem: mem, logNum: firstLog}
	db.visibleTS.Store(db.lastTS.Load())
	return nil
}

// replayWAL rebuilds mem from every segment numbered >= logNum and returns
// the first segment replayed (0 if none).
func (db *DB) replayWAL(logNum uint64, mem *memtable.Memtable) (uint64, error) {
	entries, err := os.ReadDir(db.dir)
	if err != nil {
		return 0, err
	}
	var segs []uint64
	for _, e := range entries {
		if n, ok := wal.ParseSegmentName(e.Name()); ok && n >= logNum {
			segs = append(segs, n)
		}
	}
	slices.Sort(segs)
	for i, n := range segs {
		if err := db.replaySegment(n, i == len(segs)-1, mem); err != nil {
			return 0, err
		}
	}
	if len(segs) == 0 {
		return 0, nil
	}
	return segs[0], nil
}

func (db *DB) replaySegment(num uint64, last bool, mem *memtable.Memtable) error {
	path := filepath.Join(db.dir, wal.SegmentName(num))
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := wal.NewReader(f)
	for {
		seq, payload, err := r.Next()
		if err == io.EOF {
			return nil
		}
		var ce *wal.CorruptError
		if errors.As(err, &ce) {
			if !last {
				return fmt.Errorf("%w: %s: %v", ErrCorrupted, path, err)
			}
			// The tail of the newest segment is where a crash mid-write
			// leaves a partial record. That write was never acknowledged,
			// so cutting it off is safe.
			db.log.Warn("truncating torn WAL tail", "file", path, "offset", ce.Offset, "reason", ce.Reason)
			return os.Truncate(path, ce.Offset)
		}
		if err != nil {
			return err
		}
		if _, err := db.applyRecord(mem, payload); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if seq > db.lastSeq {
			db.lastSeq = seq
		}
	}
}

// applyRecord decodes a WAL payload and inserts its operations into mem
// with the record's own timestamp. The record is validated in full before
// anything is inserted, so a bad record never leaves half a batch behind.
func (db *DB) applyRecord(mem *memtable.Memtable, payload []byte) (uint64, error) {
	ts, err := decodeBatch(payload, func(keys.Kind, []byte, []byte) {})
	if err != nil {
		return 0, err
	}
	decodeBatch(payload, func(kind keys.Kind, k, v []byte) {
		db.ikBuf = keys.Encode(db.ikBuf[:0], k, ts, kind)
		mem.Add(db.ikBuf, v)
	})
	if ts > db.lastTS.Load() {
		db.lastTS.Store(ts)
	}
	return ts, nil
}

// Close flushes the WAL and releases the directory. Data in the memtable is
// not flushed; it is recovered from the WAL on the next Open.
func (db *DB) Close() error {
	if !db.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}
	// Let in-flight commits finish. New ones will see closed and bail out.
	db.commitMu.Lock()
	for len(db.queue) > 0 {
		db.commitCond.Wait()
	}
	db.commitMu.Unlock()

	close(db.stop)
	db.wg.Wait()

	db.walMu.Lock()
	err := db.wal.Close()
	db.walMu.Unlock()
	if merr := db.man.Close(); err == nil {
		err = merr
	}
	db.tables.closeAll()
	if lerr := db.lock.Close(); err == nil {
		err = lerr
	}
	return err
}

func (db *DB) setBgErr(err error) {
	db.mu.Lock()
	if db.bgErr == nil {
		db.bgErr = err
		db.log.Error("database entered error state", "err", err)
	}
	db.flushCond.Broadcast()
	db.mu.Unlock()
}

func (db *DB) getBgErr() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.bgErr
}
