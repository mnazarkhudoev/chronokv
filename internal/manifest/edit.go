package manifest

import (
	"encoding/binary"
	"errors"
)

// FileMeta describes one SSTable. Smallest and Largest are internal keys.
type FileMeta struct {
	Num      uint64
	Size     int64
	Smallest []byte
	Largest  []byte
}

// NewFile is a table added at a level.
type NewFile struct {
	Level int
	Meta  FileMeta
}

// DeletedFile is a table removed from a level.
type DeletedFile struct {
	Level int
	Num   uint64
}

// Edit is one change to the database's file set and counters. Counter
// fields left at zero mean "unchanged"; non-zero values only ever move the
// stored value up.
type Edit struct {
	Added   []NewFile
	Deleted []DeletedFile

	LogNum      uint64 // WAL segments numbered below this are fully flushed
	NextFileNum uint64
	LastSeq     uint64
	LastTS      uint64
	Cutoff      uint64 // highest retention cutoff any compaction has applied
}

const (
	tagLogNum = iota + 1
	tagNextFileNum
	tagLastSeq
	tagLastTS
	tagCutoff
	tagAdded
	tagDeleted
)

var errBadEdit = errors.New("manifest: malformed edit")

func (e *Edit) encode() []byte {
	var b []byte
	num := func(tag int, v uint64) {
		if v != 0 {
			b = binary.AppendUvarint(b, uint64(tag))
			b = binary.AppendUvarint(b, v)
		}
	}
	num(tagLogNum, e.LogNum)
	num(tagNextFileNum, e.NextFileNum)
	num(tagLastSeq, e.LastSeq)
	num(tagLastTS, e.LastTS)
	num(tagCutoff, e.Cutoff)
	for _, f := range e.Added {
		b = binary.AppendUvarint(b, tagAdded)
		b = binary.AppendUvarint(b, uint64(f.Level))
		b = binary.AppendUvarint(b, f.Meta.Num)
		b = binary.AppendUvarint(b, uint64(f.Meta.Size))
		b = binary.AppendUvarint(b, uint64(len(f.Meta.Smallest)))
		b = append(b, f.Meta.Smallest...)
		b = binary.AppendUvarint(b, uint64(len(f.Meta.Largest)))
		b = append(b, f.Meta.Largest...)
	}
	for _, d := range e.Deleted {
		b = binary.AppendUvarint(b, tagDeleted)
		b = binary.AppendUvarint(b, uint64(d.Level))
		b = binary.AppendUvarint(b, d.Num)
	}
	return b
}

type decoder struct {
	b   []byte
	err error
}

func (d *decoder) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.err = errBadEdit
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *decoder) bytes() []byte {
	n := d.uvarint()
	if d.err != nil || n > uint64(len(d.b)) {
		d.err = errBadEdit
		return nil
	}
	v := append([]byte(nil), d.b[:n]...)
	d.b = d.b[n:]
	return v
}

func (d *decoder) level() int {
	l := d.uvarint()
	if l >= NumLevels {
		d.err = errBadEdit
	}
	return int(l)
}

func decodeEdit(b []byte) (Edit, error) {
	var e Edit
	d := &decoder{b: b}
	for len(d.b) > 0 && d.err == nil {
		switch tag := d.uvarint(); tag {
		case tagLogNum:
			e.LogNum = d.uvarint()
		case tagNextFileNum:
			e.NextFileNum = d.uvarint()
		case tagLastSeq:
			e.LastSeq = d.uvarint()
		case tagLastTS:
			e.LastTS = d.uvarint()
		case tagCutoff:
			e.Cutoff = d.uvarint()
		case tagAdded:
			var f NewFile
			f.Level = d.level()
			f.Meta.Num = d.uvarint()
			f.Meta.Size = int64(d.uvarint())
			f.Meta.Smallest = d.bytes()
			f.Meta.Largest = d.bytes()
			e.Added = append(e.Added, f)
		case tagDeleted:
			var f DeletedFile
			f.Level = d.level()
			f.Num = d.uvarint()
			e.Deleted = append(e.Deleted, f)
		default:
			d.err = errBadEdit
		}
	}
	return e, d.err
}
