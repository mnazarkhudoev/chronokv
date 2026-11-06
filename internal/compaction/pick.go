package compaction

import (
	"bytes"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
	"github.com/mnazarkhudoev/chronokv/internal/manifest"
)

// Options tune when compactions happen. Zero values pick the defaults.
type Options struct {
	L0Trigger       int   // compact L0 once it has this many tables (4)
	L1MaxBytes      int64 // size budget of L1 (10 MiB)
	LevelMultiplier int   // each level is this much bigger than the last (10)
	TargetFileSize  int64 // compaction output tables are split around this size (2 MiB)
}

func (o Options) WithDefaults() Options {
	if o.L0Trigger <= 0 {
		o.L0Trigger = 4
	}
	if o.L1MaxBytes <= 0 {
		o.L1MaxBytes = 10 << 20
	}
	if o.LevelMultiplier <= 0 {
		o.LevelMultiplier = 10
	}
	if o.TargetFileSize <= 0 {
		o.TargetFileSize = 2 << 20
	}
	return o
}

// Compaction is a set of input tables to merge into OutputLevel.
type Compaction struct {
	// Inputs, newest data first: L0 tables newest to oldest, then the
	// shallower level, then the deeper one. Merge order relies on this when
	// the same internal key shows up twice.
	Inputs      []manifest.NewFile
	OutputLevel int
	// Bottommost means no table below OutputLevel overlaps the inputs, so
	// tombstones that only hide older versions can be dropped.
	Bottommost bool
}

type keyRange struct{ lo, hi []byte } // user key prefixes, inclusive

func rangeOf(f manifest.FileMeta) keyRange {
	return keyRange{keys.Prefix(f.Smallest), keys.Prefix(f.Largest)}
}

func (r keyRange) overlaps(o keyRange) bool {
	return bytes.Compare(r.hi, o.lo) >= 0 && bytes.Compare(r.lo, o.hi) <= 0
}

func (r keyRange) union(o keyRange) keyRange {
	if r.lo == nil {
		return o
	}
	if bytes.Compare(o.lo, r.lo) < 0 {
		r.lo = o.lo
	}
	if bytes.Compare(o.hi, r.hi) > 0 {
		r.hi = o.hi
	}
	return r
}

func levelSize(files []manifest.FileMeta) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// Pick chooses the next compaction, or returns nil if nothing needs doing.
// cursor remembers, per level, where the last size-triggered compaction
// stopped, so the work is spread round-robin across a level's key space.
func Pick(v *manifest.Version, o Options, cursor *[manifest.NumLevels][]byte) *Compaction {
	o = o.WithDefaults()
	if len(v.Levels[0]) >= o.L0Trigger {
		c := &Compaction{OutputLevel: 1}
		var r keyRange
		for _, f := range v.Levels[0] {
			c.Inputs = append(c.Inputs, manifest.NewFile{Level: 0, Meta: f})
			r = r.union(rangeOf(f))
		}
		r = c.addOverlapping(v, 1, r)
		c.Bottommost = !overlapsBelow(v, 1, r)
		return c
	}

	budget := o.L1MaxBytes
	for l := 1; l < manifest.NumLevels-1; l++ {
		files := v.Levels[l]
		if levelSize(files) > budget && len(files) > 0 {
			i := 0
			if cursor[l] != nil {
				for i < len(files) && bytes.Compare(keys.Prefix(files[i].Largest), cursor[l]) <= 0 {
					i++
				}
				if i == len(files) {
					i = 0
				}
			}
			f := files[i]
			cursor[l] = append([]byte(nil), keys.Prefix(f.Largest)...)
			c := &Compaction{OutputLevel: l + 1, Inputs: []manifest.NewFile{{Level: l, Meta: f}}}
			r := c.addOverlapping(v, l+1, rangeOf(f))
			c.Bottommost = !overlapsBelow(v, l+1, r)
			return c
		}
		budget *= int64(o.LevelMultiplier)
	}
	return nil
}

// addOverlapping adds the tables at level that overlap r and returns the
// widened range.
func (c *Compaction) addOverlapping(v *manifest.Version, level int, r keyRange) keyRange {
	out := r
	for _, f := range v.Levels[level] {
		if fr := rangeOf(f); fr.overlaps(r) {
			c.Inputs = append(c.Inputs, manifest.NewFile{Level: level, Meta: f})
			out = out.union(fr)
		}
	}
	return out
}

func overlapsBelow(v *manifest.Version, level int, r keyRange) bool {
	for l := level + 1; l < manifest.NumLevels; l++ {
		for _, f := range v.Levels[l] {
			if rangeOf(f).overlaps(r) {
				return true
			}
		}
	}
	return false
}

// All returns a compaction of every table into one level: the deepest
// non-empty one, or L1 if only L0 has data. Used for manual full
// compactions. Returns nil for an empty database.
func All(v *manifest.Version) *Compaction {
	c := &Compaction{OutputLevel: 1, Bottommost: true}
	for l, files := range v.Levels {
		for _, f := range files {
			c.Inputs = append(c.Inputs, manifest.NewFile{Level: l, Meta: f})
			if l > c.OutputLevel {
				c.OutputLevel = l
			}
		}
	}
	if len(c.Inputs) == 0 {
		return nil
	}
	return c
}
