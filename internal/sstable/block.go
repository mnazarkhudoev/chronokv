package sstable

import (
	"bytes"
	"encoding/binary"
	"sort"
)

// blockBuilder builds one block. Each entry is
//
//	shared uvarint | unshared uvarint | valueLen uvarint | key[shared:] | value
//
// where shared is the length of the prefix common with the previous key.
// Every restartInterval entries the prefix compression restarts (shared=0)
// and the entry's offset is recorded, so a reader can binary search the
// restart points instead of scanning the whole block.
type blockBuilder struct {
	buf      []byte
	restarts []uint32
	counter  int
	lastKey  []byte
	entries  int
}

func (b *blockBuilder) add(key, value []byte) {
	shared := 0
	if b.counter%restartInterval == 0 {
		b.restarts = append(b.restarts, uint32(len(b.buf)))
	} else {
		n := min(len(key), len(b.lastKey))
		for shared < n && key[shared] == b.lastKey[shared] {
			shared++
		}
	}
	b.buf = binary.AppendUvarint(b.buf, uint64(shared))
	b.buf = binary.AppendUvarint(b.buf, uint64(len(key)-shared))
	b.buf = binary.AppendUvarint(b.buf, uint64(len(value)))
	b.buf = append(b.buf, key[shared:]...)
	b.buf = append(b.buf, value...)
	b.lastKey = append(b.lastKey[:0], key...)
	b.counter++
	b.entries++
}

func (b *blockBuilder) empty() bool { return b.entries == 0 }

func (b *blockBuilder) estimatedSize() int {
	return len(b.buf) + 4*len(b.restarts) + 4
}

// finish appends the restart array and returns the block contents. The
// returned slice is only valid until reset.
func (b *blockBuilder) finish() []byte {
	for _, r := range b.restarts {
		b.buf = binary.LittleEndian.AppendUint32(b.buf, r)
	}
	return binary.LittleEndian.AppendUint32(b.buf, uint32(len(b.restarts)))
}

func (b *blockBuilder) reset() {
	b.buf = b.buf[:0]
	b.restarts = b.restarts[:0]
	b.counter = 0
	b.lastKey = b.lastKey[:0]
	b.entries = 0
}

// blockIter iterates over one decoded block. Everything is bounds checked
// because the bytes come from disk; the block checksum catches random
// damage, but a reader should never panic on garbage either way.
type blockIter struct {
	data        []byte // entries region only
	restarts    []byte // numRestarts u32 values
	numRestarts int
	off         int // offset of current entry
	next        int // offset of the entry after it
	key         []byte
	value       []byte
	valid       bool
	err         error
}

func newBlockIter(block []byte) (*blockIter, error) {
	if len(block) < 4 {
		return nil, corruptf("block too short")
	}
	n := int(binary.LittleEndian.Uint32(block[len(block)-4:]))
	restartsOff := len(block) - 4 - 4*n
	if n < 0 || n > len(block)/4 || restartsOff < 0 {
		return nil, corruptf("bad restart count %d", n)
	}
	return &blockIter{
		data:        block[:restartsOff],
		restarts:    block[restartsOff : len(block)-4],
		numRestarts: n,
	}, nil
}

func (it *blockIter) restart(i int) int {
	return int(binary.LittleEndian.Uint32(it.restarts[4*i:]))
}

// parse decodes the entry at off on top of the current key (for prefix
// compression) and positions the iterator on it.
func (it *blockIter) parse(off int) bool {
	if off >= len(it.data) {
		it.valid = false
		return false
	}
	p := it.data[off:]
	shared, n1 := binary.Uvarint(p)
	if n1 <= 0 {
		return it.fail()
	}
	unshared, n2 := binary.Uvarint(p[n1:])
	if n2 <= 0 {
		return it.fail()
	}
	vlen, n3 := binary.Uvarint(p[n1+n2:])
	if n3 <= 0 {
		return it.fail()
	}
	h := n1 + n2 + n3
	if shared > uint64(len(it.key)) || unshared > uint64(len(p)-h) || vlen > uint64(len(p)-h)-unshared {
		return it.fail()
	}
	it.key = append(it.key[:shared], p[h:h+int(unshared)]...)
	vs := h + int(unshared)
	it.value = p[vs : vs+int(vlen) : vs+int(vlen)]
	it.off = off
	it.next = off + vs + int(vlen)
	it.valid = true
	return true
}

func (it *blockIter) fail() bool {
	it.err = corruptf("bad block entry")
	it.valid = false
	return false
}

func (it *blockIter) First() bool {
	it.key = it.key[:0]
	return it.parse(0)
}

func (it *blockIter) Next() bool {
	if !it.valid {
		return false
	}
	return it.parse(it.next)
}

// SeekGE positions at the first entry with key >= target.
func (it *blockIter) SeekGE(target []byte) bool {
	// Find the last restart point whose key is < target. Restart entries have
	// shared == 0, so their key can be read without context.
	i := sort.Search(it.numRestarts, func(i int) bool {
		off := it.restart(i)
		if off >= len(it.data) {
			return true
		}
		it.key = it.key[:0]
		if !it.parse(off) {
			return true
		}
		return bytes.Compare(it.key, target) >= 0
	})
	if it.err != nil {
		return false
	}
	start := 0
	if i > 0 {
		start = it.restart(i - 1)
	}
	it.key = it.key[:0]
	for ok := it.parse(start); ok; ok = it.parse(it.next) {
		if bytes.Compare(it.key, target) >= 0 {
			return true
		}
	}
	return false
}
