package iterator

import (
	"bytes"
	"errors"
	"sort"
	"testing"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// sliceIter is an Iterator over a sorted slice, for tests.
type sliceIter struct {
	ks, vs [][]byte
	pos    int
	err    error // returned once the iterator reaches failAt
	failAt int
	closed bool
}

func newSliceIter(pairs ...[2][]byte) *sliceIter {
	sort.Slice(pairs, func(i, j int) bool { return bytes.Compare(pairs[i][0], pairs[j][0]) < 0 })
	s := &sliceIter{pos: -1, failAt: -1}
	for _, p := range pairs {
		s.ks = append(s.ks, p[0])
		s.vs = append(s.vs, p[1])
	}
	return s
}

func (s *sliceIter) check() bool {
	if s.failAt >= 0 && s.pos >= s.failAt {
		s.err = errors.New("boom")
		s.pos = len(s.ks)
		return false
	}
	return s.pos >= 0 && s.pos < len(s.ks)
}
func (s *sliceIter) First() bool { s.pos = 0; return s.check() }
func (s *sliceIter) SeekGE(k []byte) bool {
	s.pos = sort.Search(len(s.ks), func(i int) bool { return bytes.Compare(s.ks[i], k) >= 0 })
	return s.check()
}
func (s *sliceIter) Next() bool    { s.pos++; return s.check() }
func (s *sliceIter) Valid() bool   { return s.err == nil && s.pos >= 0 && s.pos < len(s.ks) }
func (s *sliceIter) Key() []byte   { return s.ks[s.pos] }
func (s *sliceIter) Value() []byte { return s.vs[s.pos] }
func (s *sliceIter) Error() error  { return s.err }
func (s *sliceIter) Close() error  { s.closed = true; return nil }

func p(k, v string) [2][]byte { return [2][]byte{[]byte(k), []byte(v)} }

func drain(it Iterator) []string {
	var out []string
	for ok := it.First(); ok; ok = it.Next() {
		out = append(out, string(it.Key())+"="+string(it.Value()))
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestMergingOrder(t *testing.T) {
	m := NewMerging(
		newSliceIter(p("a", "1"), p("d", "4"), p("g", "7")),
		newSliceIter(p("b", "2"), p("e", "5")),
		newSliceIter(p("c", "3"), p("f", "6"), p("h", "8")),
	)
	eq(t, drain(m), []string{"a=1", "b=2", "c=3", "d=4", "e=5", "f=6", "g=7", "h=8"})
}

func TestMergingDuplicatePrefersFirst(t *testing.T) {
	m := NewMerging(
		newSliceIter(p("k", "new"), p("z", "1")),
		newSliceIter(p("a", "0")),
		newSliceIter(p("k", "old")),
	)
	eq(t, drain(m), []string{"a=0", "k=new", "z=1"})
}

func TestMergingSeekGE(t *testing.T) {
	m := NewMerging(
		newSliceIter(p("a", "1"), p("d", "4")),
		newSliceIter(p("b", "2"), p("e", "5")),
	)
	if !m.SeekGE([]byte("c")) || string(m.Key()) != "d" {
		t.Fatalf("SeekGE(c) landed on %q", m.Key())
	}
	if !m.Next() || string(m.Key()) != "e" || m.Next() {
		t.Fatal("Next after SeekGE broken")
	}
	if m.SeekGE([]byte("zz")) || m.Valid() {
		t.Fatal("SeekGE past the end should be invalid")
	}
}

func TestMergingPropagatesError(t *testing.T) {
	bad := newSliceIter(p("b", "2"), p("c", "3"))
	bad.failAt = 1
	m := NewMerging(newSliceIter(p("a", "1"), p("z", "9")), bad)
	got := drain(m)
	if m.Error() == nil {
		t.Fatalf("expected error, got entries %q", got)
	}
}

func TestMergingCloseClosesChildren(t *testing.T) {
	a, b := newSliceIter(), newSliceIter()
	NewMerging(a, b).Close()
	if !a.closed || !b.closed {
		t.Fatal("children not closed")
	}
}

func ver(k string, ts uint64, v string) [2][]byte {
	if v == "DEL" {
		return [2][]byte{keys.Encode(nil, []byte(k), ts, keys.KindDelete), nil}
	}
	return [2][]byte{keys.Encode(nil, []byte(k), ts, keys.KindSet), []byte(v)}
}

func visible(src Iterator, asOf uint64, start, end []byte) []string {
	v := NewVisible(src, asOf, start, end)
	defer v.Close()
	var out []string
	for v.Next() {
		out = append(out, string(v.Key())+"="+string(v.Value()))
	}
	return out
}

func TestVisibleAsOf(t *testing.T) {
	src := func() Iterator {
		return newSliceIter(
			ver("k1", 10, "a"), ver("k1", 20, "b"),
			ver("k2", 5, "x"), ver("k2", 15, "DEL"),
			ver("k3", 30, "c"),
		)
	}
	eq(t, visible(src(), 25, nil, nil), []string{"k1=b"})
	eq(t, visible(src(), 12, nil, nil), []string{"k1=a", "k2=x"})
	eq(t, visible(src(), 30, nil, nil), []string{"k1=b", "k3=c"})
	eq(t, visible(src(), 4, nil, nil), nil)
}

func TestVisibleBounds(t *testing.T) {
	src := func() Iterator {
		return newSliceIter(ver("a", 1, "1"), ver("a\x00", 1, "2"), ver("a\x00\x01", 1, "3"), ver("ab", 1, "4"), ver("b", 1, "5"))
	}
	eq(t, visible(src(), 1, []byte("a\x00"), []byte("ab")), []string{"a\x00=2", "a\x00\x01=3"})
	eq(t, visible(src(), 1, nil, nil), []string{"a=1", "a\x00=2", "a\x00\x01=3", "ab=4", "b=5"})
	eq(t, visible(src(), 1, []byte("b"), nil), []string{"b=5"})
	eq(t, visible(src(), 1, nil, []byte("a")), nil)
}

func TestVisiblePropagatesError(t *testing.T) {
	bad := newSliceIter(ver("a", 1, "1"), ver("b", 1, "2"))
	bad.failAt = 1
	v := NewVisible(bad, 10, nil, nil)
	for v.Next() {
	}
	if v.Error() == nil {
		t.Fatal("expected error")
	}
}
