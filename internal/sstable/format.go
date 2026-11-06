// Package sstable implements immutable sorted tables: the on-disk files that
// memtables are flushed into and that compaction merges.
//
// File layout:
//
//	[data block][crc] ... [data block][crc]
//	[filter][crc]
//	[index block][crc]
//	[footer: 44 bytes]
//
// Data blocks hold sorted internal keys with prefix compression and restart
// points (the LevelDB block format, more or less). The index block maps the
// last key of every data block to its location, so a lookup is: binary
// search the index, read one block, binary search inside it. The bloom
// filter is keyed on the *user key* part of internal keys, so all versions
// of a key share one filter entry and a negative answer skips the file for
// any timestamp.
package sstable

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

var (
	ErrCorrupted  = errors.New("sstable: corrupted")
	ErrOutOfOrder = errors.New("sstable: keys added out of order")
)

const (
	magic           = 0x63686b7673737431 // "chkvsst1"
	footerLen       = 44
	blockTrailerLen = 4 // crc32c after every block
	restartInterval = 16
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

func checksum(b []byte) uint32 { return crc32.Checksum(b, crcTable) }

func corruptf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupted, fmt.Sprintf(format, args...))
}

type handle struct{ off, len uint64 }

func (h handle) append(dst []byte) []byte {
	dst = binary.AppendUvarint(dst, h.off)
	return binary.AppendUvarint(dst, h.len)
}

func decodeHandle(b []byte) (handle, error) {
	off, n := binary.Uvarint(b)
	if n <= 0 {
		return handle{}, corruptf("bad block handle")
	}
	l, m := binary.Uvarint(b[n:])
	if m <= 0 {
		return handle{}, corruptf("bad block handle")
	}
	return handle{off, l}, nil
}

type footer struct{ index, filter handle }

func (f footer) encode() []byte {
	b := make([]byte, footerLen)
	binary.LittleEndian.PutUint64(b[0:], f.index.off)
	binary.LittleEndian.PutUint64(b[8:], f.index.len)
	binary.LittleEndian.PutUint64(b[16:], f.filter.off)
	binary.LittleEndian.PutUint64(b[24:], f.filter.len)
	binary.LittleEndian.PutUint64(b[32:], magic)
	binary.LittleEndian.PutUint32(b[40:], checksum(b[:40]))
	return b
}

func decodeFooter(b []byte) (footer, error) {
	if len(b) != footerLen {
		return footer{}, corruptf("short footer")
	}
	if binary.LittleEndian.Uint64(b[32:]) != magic {
		return footer{}, corruptf("bad magic number")
	}
	if checksum(b[:40]) != binary.LittleEndian.Uint32(b[40:]) {
		return footer{}, corruptf("footer checksum mismatch")
	}
	return footer{
		index:  handle{binary.LittleEndian.Uint64(b[0:]), binary.LittleEndian.Uint64(b[8:])},
		filter: handle{binary.LittleEndian.Uint64(b[16:]), binary.LittleEndian.Uint64(b[24:])},
	}, nil
}
