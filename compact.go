package chronokv

import (
	"bytes"

	"github.com/mnazarkhudoev/chronokv/internal/compaction"
	"github.com/mnazarkhudoev/chronokv/internal/iterator"
	"github.com/mnazarkhudoev/chronokv/internal/keys"
	"github.com/mnazarkhudoev/chronokv/internal/manifest"
)

// Compact flushes memtables and then merges every table into a single
// level, applying the retention window. Background compaction is paused
// while it runs. It's mostly useful for tests, benchmarks and tooling;
// normal operation doesn't need it.
func (db *DB) Compact() error {
	if err := db.Flush(); err != nil {
		return err
	}
	db.compactMu.Lock()
	defer db.compactMu.Unlock()
	v := db.man.Current()
	c := compaction.All(v)
	v.Unref()
	if c == nil {
		return nil
	}
	if err := db.runCompaction(c); err != nil {
		db.setBgErr(err)
		return err
	}
	return nil
}

func (db *DB) maybeScheduleCompaction() {
	select {
	case db.compactCh <- struct{}{}:
	default:
	}
}

func (db *DB) compactLoop() {
	defer db.wg.Done()
	for {
		select {
		case <-db.stop:
			return
		case <-db.compactCh:
			db.backgroundCompact()
		}
	}
}

func (db *DB) backgroundCompact() {
	db.compactMu.Lock()
	defer db.compactMu.Unlock()
	for !db.closed.Load() && db.getBgErr() == nil {
		v := db.man.Current()
		c := compaction.Pick(v, db.compactOpts, &db.compactCursor)
		v.Unref()
		if c == nil {
			return
		}
		if err := db.runCompaction(c); err != nil {
			db.setBgErr(err)
			return
		}
	}
}

// runCompaction merges c's inputs into new tables at c.OutputLevel and
// installs the result. Caller holds compactMu.
func (db *DB) runCompaction(c *compaction.Compaction) (err error) {
	cutoff := db.cutoff()
	target := db.compactOpts.WithDefaults().TargetFileSize

	var its []iterator.Iterator
	for _, in := range c.Inputs {
		r, err := db.tables.get(in.Meta.Num)
		if err != nil {
			return err
		}
		its = append(its, r.NewIter())
	}
	src := iterator.NewMerging(its...)
	defer src.Close()

	var (
		outputs    []manifest.FileMeta
		nums       []uint64
		b          *tableBuilder
		lastPrefix []byte
	)
	defer func() {
		if b != nil {
			b.abort()
		}
		for _, n := range nums {
			db.removePending(n)
		}
	}()

	err = compaction.Merge(src, cutoff, c.Bottommost, func(ik, value []byte) error {
		p := keys.Prefix(ik)
		// Only start a new output table between user keys: in L1 and below,
		// all versions of a key must live in the same table.
		if b != nil && b.estimatedSize() >= target && !bytes.Equal(p, lastPrefix) {
			meta, err := b.finish()
			b = nil
			if err != nil {
				return err
			}
			outputs = append(outputs, meta)
		}
		if b == nil {
			num := db.man.NewFileNum()
			db.addPending(num)
			nums = append(nums, num)
			var err error
			if b, err = db.newTableBuilder(num); err != nil {
				return err
			}
		}
		lastPrefix = append(lastPrefix[:0], p...)
		return b.add(ik, value)
	})
	if err != nil {
		return wrapTableErr("compaction", err)
	}
	if b != nil {
		meta, err := b.finish()
		b = nil
		if err != nil {
			return err
		}
		outputs = append(outputs, meta)
	}

	edit := manifest.Edit{Cutoff: cutoff}
	for _, in := range c.Inputs {
		edit.Deleted = append(edit.Deleted, manifest.DeletedFile{Level: in.Level, Num: in.Meta.Num})
	}
	for _, m := range outputs {
		edit.Added = append(edit.Added, manifest.NewFile{Level: c.OutputLevel, Meta: m})
	}
	if err := db.man.Apply(edit); err != nil {
		return err
	}
	if cutoff > db.cutoffFloor.Load() {
		db.cutoffFloor.Store(cutoff)
	}
	db.log.Debug("compaction done", "inputs", len(c.Inputs), "outputs", len(outputs), "level", c.OutputLevel)
	db.deleteObsoleteFiles(false)
	return nil
}
