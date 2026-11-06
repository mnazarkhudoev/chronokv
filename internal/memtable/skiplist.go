// Package memtable holds recent writes in memory, sorted by internal key,
// until they are flushed to an SSTable.
//
// It is a skiplist with one twist that keeps the rest of the engine simple:
// writes are serialized by the DB (only the commit leader calls Add), but
// any number of readers may iterate at the same time without locks. That
// works because a new node is fully built before it is linked in, and it
// is linked bottom level first with atomic stores. A reader that sees the
// node at any level is guaranteed to see a complete node. Nodes are never
// removed; the whole memtable is dropped after it has been flushed.
package memtable

import (
	"bytes"
	"math/rand/v2"
	"sync/atomic"
)

const (
	maxHeight = 12
	// Rough per-node bookkeeping cost, used so that ApproxSize tracks real
	// memory use a bit better than just counting key and value bytes.
	nodeOverhead = 48
)

type node struct {
	key   []byte
	value []byte
	next  []atomic.Pointer[node]
}

// Memtable is a sorted in-memory table. See the package comment for the
// concurrency contract.
type Memtable struct {
	head   *node
	height atomic.Int32
	size   atomic.Int64
	rng    *rand.Rand // only touched by the single writer
}

// New returns an empty memtable.
func New() *Memtable {
	m := &Memtable{
		head: &node{next: make([]atomic.Pointer[node], maxHeight)},
		rng:  rand.New(rand.NewPCG(0x63686b76, 0x6d656d74)),
	}
	m.height.Store(1)
	return m
}

func (m *Memtable) randomHeight() int {
	h := 1
	for h < maxHeight && m.rng.Uint32()%4 == 0 {
		h++
	}
	return h
}

// findGE returns the first node with key >= key. If prev is non-nil it is
// filled with the rightmost node before that position at every level.
func (m *Memtable) findGE(key []byte, prev *[maxHeight]*node) *node {
	x := m.head
	level := int(m.height.Load()) - 1
	for {
		next := x.next[level].Load()
		if next != nil && bytes.Compare(next.key, key) < 0 {
			x = next
			continue
		}
		if prev != nil {
			prev[level] = x
		}
		if level == 0 {
			return next
		}
		level--
	}
}

// Add inserts a copy of ik/value. Only one goroutine may call Add at a time.
// The engine never inserts the same internal key twice (timestamps are
// unique per commit), so duplicates are not handled specially.
func (m *Memtable) Add(ik, value []byte) {
	var prev [maxHeight]*node
	m.findGE(ik, &prev)

	h := m.randomHeight()
	if cur := int(m.height.Load()); h > cur {
		for i := cur; i < h; i++ {
			prev[i] = m.head
		}
		m.height.Store(int32(h))
	}

	buf := make([]byte, len(ik)+len(value))
	copy(buf, ik)
	copy(buf[len(ik):], value)
	n := &node{
		key:   buf[:len(ik):len(ik)],
		value: buf[len(ik):],
		next:  make([]atomic.Pointer[node], h),
	}
	for i := 0; i < h; i++ {
		n.next[i].Store(prev[i].next[i].Load())
		prev[i].next[i].Store(n)
	}
	m.size.Add(int64(len(ik)+len(value)) + nodeOverhead + int64(8*h))
}

// ApproxSize is an estimate of the memory used by entries.
func (m *Memtable) ApproxSize() int64 { return m.size.Load() }

// Empty reports whether nothing has been added yet.
func (m *Memtable) Empty() bool { return m.head.next[0].Load() == nil }

// NewIter returns an unpositioned iterator. Iterators are cheap and may be
// used concurrently with Add.
func (m *Memtable) NewIter() *Iter { return &Iter{m: m} }

// Iter walks a memtable in key order.
type Iter struct {
	m *Memtable
	n *node
}

func (it *Iter) First() bool {
	it.n = it.m.head.next[0].Load()
	return it.n != nil
}

func (it *Iter) SeekGE(key []byte) bool {
	it.n = it.m.findGE(key, nil)
	return it.n != nil
}

func (it *Iter) Next() bool {
	if it.n != nil {
		it.n = it.n.next[0].Load()
	}
	return it.n != nil
}

func (it *Iter) Valid() bool   { return it.n != nil }
func (it *Iter) Key() []byte   { return it.n.key }
func (it *Iter) Value() []byte { return it.n.value }
func (it *Iter) Error() error  { return nil }
func (it *Iter) Close() error  { it.n = nil; return nil }
