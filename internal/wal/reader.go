package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// CorruptError reports a record that could not be read. Offset is where the
// bad record starts, which is also where a torn log should be truncated.
type CorruptError struct {
	Offset int64
	Reason string
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("wal: corrupt record at offset %d: %s", e.Offset, e.Reason)
}

// Reader reads records sequentially. It does not decide whether a corrupt
// record is a harmless torn tail or real damage; that depends on where the
// segment sits in the log, which only the caller knows.
type Reader struct {
	br     *bufio.Reader
	offset int64
	hdr    [HeaderSize]byte
	buf    []byte
}

// NewReader returns a Reader over r.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64<<10)}
}

// Next returns the next record. The payload is only valid until the next
// call. At a clean end of input it returns io.EOF; anything else that stops
// reading is a *CorruptError.
func (r *Reader) Next() (seq uint64, payload []byte, err error) {
	n, err := io.ReadFull(r.br, r.hdr[:])
	switch {
	case err == io.EOF:
		return 0, nil, io.EOF
	case errors.Is(err, io.ErrUnexpectedEOF):
		return 0, nil, r.corrupt(fmt.Sprintf("short header (%d bytes)", n))
	case err != nil:
		return 0, nil, err
	}
	length := binary.LittleEndian.Uint32(r.hdr[0:4])
	sum := binary.LittleEndian.Uint32(r.hdr[4:8])
	seq = binary.LittleEndian.Uint64(r.hdr[8:16])
	if length > MaxRecordSize {
		return 0, nil, r.corrupt(fmt.Sprintf("length %d exceeds limit", length))
	}
	if cap(r.buf) < int(length) {
		r.buf = make([]byte, length)
	}
	r.buf = r.buf[:length]
	if _, err := io.ReadFull(r.br, r.buf); err != nil {
		if err == io.EOF || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, r.corrupt("short payload")
		}
		return 0, nil, err
	}
	if checksum(seq, r.buf) != sum {
		return 0, nil, r.corrupt("checksum mismatch")
	}
	r.offset += HeaderSize + int64(length)
	return seq, r.buf, nil
}

// Offset is the byte offset just past the last record that read cleanly.
func (r *Reader) Offset() int64 { return r.offset }

func (r *Reader) corrupt(reason string) error {
	return &CorruptError{Offset: r.offset, Reason: reason}
}
