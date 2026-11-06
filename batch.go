package chronokv

import (
	"encoding/binary"
	"fmt"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// maxBatchSize caps one commit. It keeps WAL records well under the WAL's
// own limit and stops one giant batch from holding the commit pipeline.
const maxBatchSize = 32 << 20

type batchOp struct {
	kind  keys.Kind
	key   []byte
	value []byte
}

// Batch collects writes that commit atomically: they share one timestamp,
// and a reader at any point in time sees either all of them or none.
//
// Writing the same key twice in a batch keeps only the last write. All
// writes in a batch share a timestamp, so two versions of one key in the
// same batch would be indistinguishable.
type Batch struct {
	ops   []batchOp
	index map[string]int // key -> position in ops
	size  int
}

// Put adds a write of key=value. The batch keeps its own copies.
func (b *Batch) Put(key, value []byte) { b.add(keys.KindSet, key, value) }

// Delete adds a deletion of key.
func (b *Batch) Delete(key []byte) { b.add(keys.KindDelete, key, nil) }

func (b *Batch) add(kind keys.Kind, key, value []byte) {
	buf := make([]byte, len(key)+len(value))
	copy(buf, key)
	copy(buf[len(key):], value)
	op := batchOp{kind, buf[:len(key):len(key)], buf[len(key):]}
	if b.index == nil {
		b.index = map[string]int{}
	}
	if i, ok := b.index[string(key)]; ok {
		b.size -= opSize(b.ops[i])
		b.ops[i] = op
	} else {
		b.index[string(key)] = len(b.ops)
		b.ops = append(b.ops, op)
	}
	b.size += opSize(op)
}

func opSize(op batchOp) int {
	return 1 + 2*binary.MaxVarintLen64 + len(op.key) + len(op.value)
}

// Len is the number of operations in the batch.
func (b *Batch) Len() int { return len(b.ops) }

// Reset empties the batch so it can be reused.
func (b *Batch) Reset() {
	b.ops = b.ops[:0]
	clear(b.index)
	b.size = 0
}

// encodeBatch produces the WAL payload for b:
//
//	ts u64 | count uvarint | { kind u8 | keyLen uvarint | key | valueLen uvarint | value }*
//
// The timestamp is part of the record so that replaying the log (or a
// replica applying it) reproduces exactly the same versions.
func encodeBatch(dst []byte, ts uint64, b *Batch) []byte {
	dst = binary.LittleEndian.AppendUint64(dst, ts)
	dst = binary.AppendUvarint(dst, uint64(len(b.ops)))
	for _, op := range b.ops {
		dst = append(dst, byte(op.kind))
		dst = binary.AppendUvarint(dst, uint64(len(op.key)))
		dst = append(dst, op.key...)
		dst = binary.AppendUvarint(dst, uint64(len(op.value)))
		dst = append(dst, op.value...)
	}
	return dst
}

func batchCorrupt(what string) error {
	return fmt.Errorf("%w: bad batch record: %s", ErrCorrupted, what)
}

// decodeBatch parses a WAL payload, calling fn for each operation in order.
// The slices passed to fn alias p.
func decodeBatch(p []byte, fn func(kind keys.Kind, key, value []byte)) (ts uint64, err error) {
	if len(p) < 8 {
		return 0, batchCorrupt("short header")
	}
	ts = binary.LittleEndian.Uint64(p)
	p = p[8:]
	count, n := binary.Uvarint(p)
	if n <= 0 {
		return 0, batchCorrupt("bad count")
	}
	p = p[n:]
	readBytes := func() ([]byte, bool) {
		l, n := binary.Uvarint(p)
		if n <= 0 || l > uint64(len(p)-n) {
			return nil, false
		}
		b := p[n : n+int(l)]
		p = p[n+int(l):]
		return b, true
	}
	for i := uint64(0); i < count; i++ {
		if len(p) < 1 {
			return 0, batchCorrupt("truncated")
		}
		kind := keys.Kind(p[0])
		if kind != keys.KindSet && kind != keys.KindDelete {
			return 0, batchCorrupt("bad op kind")
		}
		p = p[1:]
		k, ok := readBytes()
		if !ok {
			return 0, batchCorrupt("truncated key")
		}
		v, ok := readBytes()
		if !ok {
			return 0, batchCorrupt("truncated value")
		}
		fn(kind, k, v)
	}
	if len(p) != 0 {
		return 0, batchCorrupt("trailing bytes")
	}
	return ts, nil
}
