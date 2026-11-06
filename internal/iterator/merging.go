package iterator

import (
	"bytes"
	"container/heap"
)

// Merging merges several sorted iterators into one. Sources are given newest
// first; if two sources hold the exact same internal key, the one from the
// earlier source wins and the others are skipped. (The engine should never
// produce such duplicates, but recovery and compaction edge cases are much
// easier to reason about when the merge is defined to tolerate them.)
type Merging struct {
	iters []Iterator
	h     mergeHeap
	err   error
	prev  []byte
}

// NewMerging returns an unpositioned merging iterator over iters.
func NewMerging(iters ...Iterator) *Merging {
	m := &Merging{iters: iters}
	m.h.iters = iters
	return m
}

type mergeHeap struct {
	iters []Iterator
	idx   []int
}

func (h *mergeHeap) Len() int { return len(h.idx) }
func (h *mergeHeap) Less(i, j int) bool {
	a, b := h.idx[i], h.idx[j]
	if c := bytes.Compare(h.iters[a].Key(), h.iters[b].Key()); c != 0 {
		return c < 0
	}
	return a < b
}
func (h *mergeHeap) Swap(i, j int) { h.idx[i], h.idx[j] = h.idx[j], h.idx[i] }
func (h *mergeHeap) Push(x any)    { h.idx = append(h.idx, x.(int)) }
func (h *mergeHeap) Pop() any {
	x := h.idx[len(h.idx)-1]
	h.idx = h.idx[:len(h.idx)-1]
	return x
}

// rebuild puts every valid child into the heap and records child errors.
func (m *Merging) rebuild() bool {
	m.h.idx = m.h.idx[:0]
	for i, it := range m.iters {
		if it.Valid() {
			m.h.idx = append(m.h.idx, i)
		} else if err := it.Error(); err != nil && m.err == nil {
			m.err = err
		}
	}
	heap.Init(&m.h)
	return m.Valid()
}

func (m *Merging) First() bool {
	for _, it := range m.iters {
		it.First()
	}
	return m.rebuild()
}

func (m *Merging) SeekGE(key []byte) bool {
	for _, it := range m.iters {
		it.SeekGE(key)
	}
	return m.rebuild()
}

// advanceTop moves the child on top of the heap forward one entry.
func (m *Merging) advanceTop() {
	top := m.h.idx[0]
	if m.iters[top].Next() {
		heap.Fix(&m.h, 0)
		return
	}
	if err := m.iters[top].Error(); err != nil && m.err == nil {
		m.err = err
	}
	heap.Pop(&m.h)
}

func (m *Merging) Next() bool {
	if !m.Valid() {
		return false
	}
	m.prev = append(m.prev[:0], m.Key()...)
	m.advanceTop()
	// Skip exact duplicates of the key we just left.
	for m.Valid() && bytes.Equal(m.Key(), m.prev) {
		m.advanceTop()
	}
	return m.Valid()
}

func (m *Merging) Valid() bool   { return m.err == nil && len(m.h.idx) > 0 }
func (m *Merging) Key() []byte   { return m.iters[m.h.idx[0]].Key() }
func (m *Merging) Value() []byte { return m.iters[m.h.idx[0]].Value() }
func (m *Merging) Error() error  { return m.err }

func (m *Merging) Close() error {
	var err error
	for _, it := range m.iters {
		if cerr := it.Close(); err == nil {
			err = cerr
		}
	}
	m.h.idx = nil
	return err
}
