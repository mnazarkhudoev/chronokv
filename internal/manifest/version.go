package manifest

import (
	"bytes"
	"slices"
	"sync/atomic"
)

// NumLevels is the number of LSM levels (L0..L6).
const NumLevels = 7

// Version is an immutable snapshot of which tables make up the database.
//
// L0 tables come straight from memtable flushes, so their key ranges
// overlap; they are kept newest first. Tables in L1 and below never overlap
// within a level and are sorted by Smallest.
//
// Readers and iterators hold a reference to the Version they started with.
// As long as any reference exists the tables in it are not deleted, even if
// compaction has replaced them in newer versions.
type Version struct {
	Levels [NumLevels][]FileMeta

	refs atomic.Int32
	m    *Manifest
}

// Ref adds a reference.
func (v *Version) Ref() { v.refs.Add(1) }

// Unref drops a reference. When the last one goes, the manifest is told so
// it can let the DB delete files nobody needs anymore.
func (v *Version) Unref() {
	switch n := v.refs.Add(-1); {
	case n == 0:
		if v.m != nil {
			v.m.release(v)
		}
	case n < 0:
		panic("manifest: Version.Unref without matching Ref")
	}
}

// apply returns a new version with e applied on top of v.
func (v *Version) apply(e *Edit) *Version {
	nv := &Version{}
	deleted := make(map[[2]uint64]bool, len(e.Deleted))
	for _, d := range e.Deleted {
		deleted[[2]uint64{uint64(d.Level), d.Num}] = true
	}
	for l := range v.Levels {
		for _, f := range v.Levels[l] {
			if !deleted[[2]uint64{uint64(l), f.Num}] {
				nv.Levels[l] = append(nv.Levels[l], f)
			}
		}
	}
	for _, a := range e.Added {
		nv.Levels[a.Level] = append(nv.Levels[a.Level], a.Meta)
	}
	slices.SortFunc(nv.Levels[0], func(a, b FileMeta) int {
		switch {
		case a.Num > b.Num:
			return -1
		case a.Num < b.Num:
			return 1
		}
		return 0
	})
	for l := 1; l < NumLevels; l++ {
		slices.SortFunc(nv.Levels[l], func(a, b FileMeta) int { return bytes.Compare(a.Smallest, b.Smallest) })
	}
	return nv
}

// snapshotEdit is an edit that recreates v (and the counters) from nothing.
func (v *Version) snapshotEdit(s State) Edit {
	e := Edit{LogNum: s.LogNum, NextFileNum: s.NextFileNum, LastSeq: s.LastSeq, LastTS: s.LastTS, Cutoff: s.Cutoff}
	for l := range v.Levels {
		for _, f := range v.Levels[l] {
			e.Added = append(e.Added, NewFile{Level: l, Meta: f})
		}
	}
	return e
}
