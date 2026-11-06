package sstable

import (
	"bytes"
	"encoding/binary"
	"io"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// WriterOptions tune the table layout. Zero values pick the defaults.
type WriterOptions struct {
	BlockSize       int // target uncompressed data block size, default 4096
	BloomBitsPerKey int // default 10 (~1% false positives)
}

// Props describes a finished table.
type Props struct {
	Smallest, Largest []byte
	Entries           int
	Size              int64
}

// Writer streams sorted entries into a table.
type Writer struct {
	w          io.Writer
	opts       WriterOptions
	data       blockBuilder
	index      blockBuilder
	offset     int64
	hashes     []uint64
	lastPrefix []byte
	props      Props
	err        error
}

// NewWriter returns a Writer that writes the table to w.
func NewWriter(w io.Writer, opts WriterOptions) *Writer {
	if opts.BlockSize <= 0 {
		opts.BlockSize = 4096
	}
	if opts.BloomBitsPerKey <= 0 {
		opts.BloomBitsPerKey = 10
	}
	return &Writer{w: w, opts: opts}
}

// Add appends an entry. Keys must be strictly increasing.
func (w *Writer) Add(ik, value []byte) error {
	if w.err != nil {
		return w.err
	}
	if w.props.Entries > 0 && bytes.Compare(ik, w.props.Largest) <= 0 {
		return ErrOutOfOrder
	}
	// Bloom on the user key part. Keys that aren't internal keys (only in
	// tests) are hashed whole.
	p := keys.Prefix(ik)
	if p == nil {
		p = ik
	}
	if w.props.Entries == 0 || !bytes.Equal(p, w.lastPrefix) {
		w.hashes = append(w.hashes, bloomHash(p))
		w.lastPrefix = append(w.lastPrefix[:0], p...)
	}
	if w.props.Entries == 0 {
		w.props.Smallest = append([]byte(nil), ik...)
	}
	w.props.Largest = append(w.props.Largest[:0], ik...)
	w.props.Entries++

	w.data.add(ik, value)
	if w.data.estimatedSize() >= w.opts.BlockSize {
		return w.flushBlock()
	}
	return nil
}

func (w *Writer) writeBlock(b []byte) (handle, error) {
	h := handle{off: uint64(w.offset), len: uint64(len(b))}
	b = binary.LittleEndian.AppendUint32(b, checksum(b))
	if _, err := w.w.Write(b); err != nil {
		w.err = err
		return h, err
	}
	w.offset += int64(len(b))
	return h, nil
}

func (w *Writer) flushBlock() error {
	if w.data.empty() {
		return nil
	}
	h, err := w.writeBlock(w.data.finish())
	if err != nil {
		return err
	}
	// The index key is the last key in the block: the block holds every key
	// <= it that is > the previous block's last key.
	w.index.add(w.data.lastKey, h.append(nil))
	w.data.reset()
	return nil
}

// EstimatedSize is roughly how big the file would be if finished now.
func (w *Writer) EstimatedSize() int64 {
	return w.offset + int64(w.data.estimatedSize()+w.index.estimatedSize()) +
		int64(len(w.hashes)*w.opts.BloomBitsPerKey/8) + footerLen
}

// Finish writes the filter, index and footer. The Writer must not be used
// afterwards.
func (w *Writer) Finish() (Props, error) {
	if err := w.flushBlock(); err != nil {
		return Props{}, err
	}
	fh, err := w.writeBlock(buildFilter(w.hashes, w.opts.BloomBitsPerKey))
	if err != nil {
		return Props{}, err
	}
	ih, err := w.writeBlock(w.index.finish())
	if err != nil {
		return Props{}, err
	}
	if _, err := w.w.Write(footer{index: ih, filter: fh}.encode()); err != nil {
		w.err = err
		return Props{}, err
	}
	w.offset += footerLen
	w.props.Size = w.offset
	return w.props, nil
}
