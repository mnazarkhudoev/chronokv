package compaction

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
	"github.com/mnazarkhudoev/chronokv/internal/manifest"
)

type sliceIter struct {
	ks, vs [][]byte
	pos    int
}

func (s *sliceIter) First() bool { s.pos = 0; return s.Valid() }
func (s *sliceIter) SeekGE(k []byte) bool {
	s.pos = sort.Search(len(s.ks), func(i int) bool { return bytes.Compare(s.ks[i], k) >= 0 })
	return s.Valid()
}
func (s *sliceIter) Next() bool    { s.pos++; return s.Valid() }
func (s *sliceIter) Valid() bool   { return s.pos >= 0 && s.pos < len(s.ks) }
func (s *sliceIter) Key() []byte   { return s.ks[s.pos] }
func (s *sliceIter) Value() []byte { return s.vs[s.pos] }
func (s *sliceIter) Error() error  { return nil }
func (s *sliceIter) Close() error  { return nil }

// v builds an entry: "k@150S" style specs.
type spec struct {
	k   string
	ts  uint64
	del bool
}

func input(specs ...spec) *sliceIter {
	s := &sliceIter{}
	for _, sp := range specs {
		kind := keys.KindSet
		if sp.del {
			kind = keys.KindDelete
		}
		s.ks = append(s.ks, keys.Encode(nil, []byte(sp.k), sp.ts, kind))
		s.vs = append(s.vs, []byte(fmt.Sprint(sp.ts)))
	}
	sort.SliceStable(s.ks, func(i, j int) bool { return bytes.Compare(s.ks[i], s.ks[j]) < 0 })
	return s
}

func merged(t *testing.T, src *sliceIter, cutoff uint64, bottommost bool) []string {
	t.Helper()
	var out []string
	err := Merge(src, cutoff, bottommost, func(ik, value []byte) error {
		k, ts, kind, err := keys.Decode(ik)
		if err != nil {
			t.Fatal(err)
		}
		s := fmt.Sprintf("%s@%d", k, ts)
		if kind == keys.KindDelete {
			s += "D"
		}
		out = append(out, s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeRetention(t *testing.T) {
	eq(t, merged(t, input(spec{"k", 150, false}, spec{"k", 120, false}, spec{"k", 90, false}, spec{"k", 80, false}), 100, false),
		"k@150", "k@120", "k@90")
	eq(t, merged(t, input(spec{"k", 150, false}, spec{"k", 90, true}, spec{"k", 80, false}), 100, true),
		"k@150")
	eq(t, merged(t, input(spec{"k", 150, false}, spec{"k", 90, true}, spec{"k", 80, false}), 100, false),
		"k@150", "k@90D")
	eq(t, merged(t, input(spec{"k", 150, false}, spec{"k", 90, true}, spec{"k", 80, false}), 0, true),
		"k@150", "k@90D", "k@80")
	// Two keys interleaved: the "seen an old version" state resets per key.
	eq(t, merged(t, input(spec{"a", 50, false}, spec{"a", 40, false}, spec{"b", 60, false}, spec{"b", 30, false}), 100, false),
		"a@50", "b@60")
	// A key whose only old version is a tombstone disappears entirely at the bottom.
	eq(t, merged(t, input(spec{"a", 50, true}, spec{"b", 150, false}), 100, true), "b@150")
}

func TestMergeDropsDuplicates(t *testing.T) {
	s := input(spec{"k", 150, false}, spec{"k", 120, false})
	s.ks = append(s.ks[:1], append([][]byte{s.ks[0]}, s.ks[1:]...)...)
	s.vs = append(s.vs[:1], append([][]byte{s.vs[0]}, s.vs[1:]...)...)
	eq(t, merged(t, s, 0, false), "k@150", "k@120")
}

func fm(num uint64, size int64, small, large string) manifest.FileMeta {
	return manifest.FileMeta{
		Num: num, Size: size,
		Smallest: keys.Encode(nil, []byte(small), 100, keys.KindSet),
		Largest:  keys.Encode(nil, []byte(large), 1, keys.KindSet),
	}
}

func inputNums(c *Compaction) []string {
	var out []string
	for _, in := range c.Inputs {
		out = append(out, fmt.Sprintf("L%d#%d", in.Level, in.Meta.Num))
	}
	return out
}

func TestPickL0(t *testing.T) {
	v := &manifest.Version{}
	v.Levels[0] = []manifest.FileMeta{fm(9, 10, "b", "c"), fm(8, 10, "c", "d"), fm(7, 10, "b", "b"), fm(6, 10, "b", "d")}
	v.Levels[1] = []manifest.FileMeta{fm(1, 10, "a", "a"), fm(2, 10, "b", "c"), fm(3, 10, "e", "f")}
	var cursor [manifest.NumLevels][]byte
	c := Pick(v, Options{}, &cursor)
	if c == nil {
		t.Fatal("expected an L0 compaction")
	}
	eq(t, inputNums(c), "L0#9", "L0#8", "L0#7", "L0#6", "L1#2")
	if c.OutputLevel != 1 || !c.Bottommost {
		t.Fatalf("output level %d bottommost %v", c.OutputLevel, c.Bottommost)
	}

	v.Levels[0] = v.Levels[0][:3]
	if c := Pick(v, Options{}, &cursor); c != nil {
		t.Fatalf("3 L0 files and small levels should not compact, got %v", inputNums(c))
	}
}

func TestPickLevelSize(t *testing.T) {
	v := &manifest.Version{}
	v.Levels[1] = []manifest.FileMeta{fm(1, 80, "a", "b"), fm(2, 80, "d", "e"), fm(3, 80, "g", "h")}
	v.Levels[2] = []manifest.FileMeta{fm(10, 50, "a", "a"), fm(11, 50, "b", "d"), fm(12, 50, "f", "z")}
	o := Options{L1MaxBytes: 100}
	var cursor [manifest.NumLevels][]byte

	c := Pick(v, o, &cursor)
	eq(t, inputNums(c), "L1#1", "L2#10", "L2#11")
	if c.OutputLevel != 2 {
		t.Fatalf("output level %d", c.OutputLevel)
	}
	c = Pick(v, o, &cursor)
	eq(t, inputNums(c), "L1#2", "L2#11")
	c = Pick(v, o, &cursor)
	eq(t, inputNums(c), "L1#3", "L2#12")
	c = Pick(v, o, &cursor) // wraps around
	eq(t, inputNums(c), "L1#1", "L2#10", "L2#11")
}

func TestBottommost(t *testing.T) {
	v := &manifest.Version{}
	v.Levels[1] = []manifest.FileMeta{fm(1, 500, "b", "c")}
	v.Levels[3] = []manifest.FileMeta{fm(5, 10, "c", "c")}
	o := Options{L1MaxBytes: 100}
	var cursor [manifest.NumLevels][]byte
	if c := Pick(v, o, &cursor); c == nil || c.Bottommost {
		t.Fatalf("overlap in L3 means not bottommost: %+v", c)
	}
	v.Levels[3] = []manifest.FileMeta{fm(5, 10, "x", "z")}
	cursor = [manifest.NumLevels][]byte{}
	if c := Pick(v, o, &cursor); c == nil || !c.Bottommost {
		t.Fatalf("no deeper overlap means bottommost: %+v", c)
	}
}

func TestAll(t *testing.T) {
	v := &manifest.Version{}
	if All(v) != nil {
		t.Fatal("All on an empty version should be nil")
	}
	v.Levels[0] = []manifest.FileMeta{fm(9, 10, "a", "b")}
	v.Levels[3] = []manifest.FileMeta{fm(2, 10, "a", "z")}
	c := All(v)
	eq(t, inputNums(c), "L0#9", "L3#2")
	if c.OutputLevel != 3 || !c.Bottommost {
		t.Fatalf("All: output %d bottommost %v", c.OutputLevel, c.Bottommost)
	}
	v = &manifest.Version{}
	v.Levels[0] = []manifest.FileMeta{fm(9, 10, "a", "b")}
	if c := All(v); c.OutputLevel != 1 {
		t.Fatalf("L0-only All should output to L1, got %d", c.OutputLevel)
	}
}
