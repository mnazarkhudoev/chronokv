package sstable

import (
	"encoding/binary"
	"io"
)

// File is what a Reader reads from; *os.File satisfies it.
type File interface {
	io.ReaderAt
	io.Closer
}

// Reader gives access to one table. The index and filter stay in memory;
// data blocks are read from the file on demand. A Reader is safe for
// concurrent use (each iterator has its own state).
type Reader struct {
	f      File
	size   int64
	index  []byte
	filter []byte
}

// Open reads the footer, index and filter of the table in f.
func Open(f File, size int64) (*Reader, error) {
	if size < footerLen {
		return nil, corruptf("file too small (%d bytes)", size)
	}
	buf := make([]byte, footerLen)
	if _, err := f.ReadAt(buf, size-footerLen); err != nil {
		return nil, err
	}
	ft, err := decodeFooter(buf)
	if err != nil {
		return nil, err
	}
	r := &Reader{f: f, size: size - footerLen}
	if r.index, err = r.readBlock(ft.index); err != nil {
		return nil, err
	}
	if _, err := newBlockIter(r.index); err != nil {
		return nil, err
	}
	if r.filter, err = r.readBlock(ft.filter); err != nil {
		return nil, err
	}
	return r, nil
}

// readBlock reads the block at h and verifies its checksum.
func (r *Reader) readBlock(h handle) ([]byte, error) {
	end := h.off + h.len + blockTrailerLen
	if h.off > uint64(r.size) || h.len > uint64(r.size) || end > uint64(r.size) {
		return nil, corruptf("block handle %d+%d out of range", h.off, h.len)
	}
	buf := make([]byte, h.len+blockTrailerLen)
	if _, err := r.f.ReadAt(buf, int64(h.off)); err != nil {
		return nil, err
	}
	data := buf[:h.len]
	if checksum(data) != binary.LittleEndian.Uint32(buf[h.len:]) {
		return nil, corruptf("block at offset %d: checksum mismatch", h.off)
	}
	return data, nil
}

// MayContain reports whether the table might hold any version of the user
// key whose escaped prefix is given (see keys.Prefix). False means
// definitely not.
func (r *Reader) MayContain(prefix []byte) bool {
	return filterMayContain(r.filter, bloomHash(prefix))
}

// NewIter returns an unpositioned iterator over the whole table.
func (r *Reader) NewIter() *Iter {
	idx, err := newBlockIter(r.index)
	return &Iter{r: r, index: idx, err: err}
}

// Close closes the underlying file.
func (r *Reader) Close() error { return r.f.Close() }
