// Package wal is the write-ahead log: an append-only file of checksummed
// records. Every committed batch goes here before it touches the memtable,
// so after a crash the memtable can be rebuilt by replaying the log.
//
// Record layout (little-endian):
//
//	length u32 | crc32c u32 | seq u64 | payload [length]byte
//
// The checksum covers seq and payload. The log is split into numbered
// segment files (000123.wal) so that old segments can simply be deleted
// once their contents have been flushed to an SSTable.
//
// The same framing is reused by the manifest.
package wal

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HeaderSize is the size of a record header in bytes.
const HeaderSize = 16

// MaxRecordSize caps a single payload. Anything larger in a header is treated
// as corruption rather than trusted, so a damaged length can't make the
// reader allocate gigabytes.
const MaxRecordSize = 64 << 20

var crcTable = crc32.MakeTable(crc32.Castagnoli)

func checksum(seq uint64, payload []byte) uint32 {
	var s [8]byte
	binary.LittleEndian.PutUint64(s[:], seq)
	c := crc32.Update(0, crcTable, s[:])
	return crc32.Update(c, crcTable, payload)
}

// SegmentName returns the file name for segment num.
func SegmentName(num uint64) string { return fmt.Sprintf("%06d.wal", num) }

// ParseSegmentName is the inverse of SegmentName.
func ParseSegmentName(name string) (uint64, bool) {
	base, ok := strings.CutSuffix(name, ".wal")
	if !ok || base == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(base, 10, 64)
	return n, err == nil
}

// Writer appends records to one segment file. It is not safe for concurrent
// use; the DB serializes writes through its commit pipeline.
type Writer struct {
	f    *os.File
	bw   *bufio.Writer
	size int64
	hdr  [HeaderSize]byte
}

// Create creates a new segment at path. It refuses to overwrite an existing
// file: reusing a segment number would mean a bug elsewhere, and silently
// truncating a log is the one thing a log must never do.
func Create(path string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := SyncDir(filepath.Dir(path)); err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{f: f, bw: bufio.NewWriterSize(f, 64<<10)}, nil
}

// Append buffers one record. Call Flush or Sync to make it durable.
func (w *Writer) Append(seq uint64, payload []byte) error {
	if len(payload) > MaxRecordSize {
		return fmt.Errorf("wal: record of %d bytes exceeds limit", len(payload))
	}
	binary.LittleEndian.PutUint32(w.hdr[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(w.hdr[4:8], checksum(seq, payload))
	binary.LittleEndian.PutUint64(w.hdr[8:16], seq)
	if _, err := w.bw.Write(w.hdr[:]); err != nil {
		return err
	}
	if _, err := w.bw.Write(payload); err != nil {
		return err
	}
	w.size += HeaderSize + int64(len(payload))
	return nil
}

// Flush hands buffered records to the OS without forcing them to disk.
func (w *Writer) Flush() error { return w.bw.Flush() }

// Sync flushes and fsyncs.
func (w *Writer) Sync() error {
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

// Size is the number of bytes appended so far, including buffered ones.
func (w *Writer) Size() int64 { return w.size }

// Close syncs and closes the segment.
func (w *Writer) Close() error {
	err := w.Sync()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// SyncDir fsyncs a directory so that file creations and renames inside it
// survive a crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
