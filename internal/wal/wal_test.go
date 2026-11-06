package wal

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

type rec struct {
	seq     uint64
	payload []byte
}

// writeRecords writes recs to a fresh segment and returns the file contents
// and the starting offset of each record.
func writeRecords(t *testing.T, recs []rec) ([]byte, []int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), SegmentName(1))
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var offsets []int64
	for _, r := range recs {
		offsets = append(offsets, w.Size())
		if err := w.Append(r.seq, r.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != w.Size() {
		t.Fatalf("Size() = %d, file has %d bytes", w.Size(), len(data))
	}
	return data, offsets
}

func randRecords(n int, maxLen int) []rec {
	rng := rand.New(rand.NewPCG(1, 2))
	recs := make([]rec, n)
	for i := range recs {
		p := make([]byte, rng.IntN(maxLen+1))
		for j := range p {
			p[j] = byte(rng.Uint32())
		}
		recs[i] = rec{seq: uint64(i + 1), payload: p}
	}
	return recs
}

// readAll returns the records read before the first error, and that error.
func readAll(data []byte) ([]rec, *Reader, error) {
	r := NewReader(bytes.NewReader(data))
	var out []rec
	for {
		seq, p, err := r.Next()
		if err != nil {
			return out, r, err
		}
		out = append(out, rec{seq, append([]byte(nil), p...)})
	}
}

func TestRoundTrip(t *testing.T) {
	recs := randRecords(1000, 10<<10)
	recs[3].payload = nil // make sure empty payloads work
	data, _ := writeRecords(t, recs)
	got, _, err := readAll(data)
	if err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if len(got) != len(recs) {
		t.Fatalf("read %d records, want %d", len(got), len(recs))
	}
	for i := range recs {
		if got[i].seq != recs[i].seq || !bytes.Equal(got[i].payload, recs[i].payload) {
			t.Fatalf("record %d differs", i)
		}
	}
}

func TestTornTail(t *testing.T) {
	recs := randRecords(5, 300)
	data, offsets := writeRecords(t, recs)
	start := offsets[4]
	for cut := start + 1; cut < int64(len(data)); cut++ {
		got, r, err := readAll(data[:cut])
		var ce *CorruptError
		if !errors.As(err, &ce) {
			t.Fatalf("cut %d: err = %v, want *CorruptError", cut, err)
		}
		if len(got) != 4 || ce.Offset != start || r.Offset() != start {
			t.Fatalf("cut %d: got %d records, corrupt offset %d, reader offset %d; want 4, %d, %d",
				cut, len(got), ce.Offset, r.Offset(), start, start)
		}
	}
}

func TestBitFlip(t *testing.T) {
	recs := randRecords(5, 300)
	recs[2].payload = []byte("hello, this payload gets damaged")
	data, offsets := writeRecords(t, recs)
	data[offsets[2]+HeaderSize+3] ^= 0x40
	got, _, err := readAll(data)
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Offset != offsets[2] || len(got) != 2 {
		t.Fatalf("got %d records, err %v; want 2 records and corruption at %d", len(got), err, offsets[2])
	}
}

func TestEmpty(t *testing.T) {
	if _, _, err := NewReader(bytes.NewReader(nil)).Next(); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestOversizedLength(t *testing.T) {
	data, _ := writeRecords(t, []rec{{1, []byte("x")}})
	// Overwrite the length field with something absurd.
	data[0], data[1], data[2], data[3] = 0x01, 0x00, 0x00, 0x04 // little-endian 0x04000001 = MaxRecordSize+1
	_, _, err := readAll(data)
	var ce *CorruptError
	if !errors.As(err, &ce) || ce.Offset != 0 {
		t.Fatalf("err = %v, want *CorruptError at 0", err)
	}
}

func TestSegmentNames(t *testing.T) {
	if got := SegmentName(42); got != "000042.wal" {
		t.Fatalf("SegmentName(42) = %q", got)
	}
	if n, ok := ParseSegmentName("000042.wal"); !ok || n != 42 {
		t.Fatalf("ParseSegmentName = %d, %v", n, ok)
	}
	for _, bad := range []string{"000042.sst", "abc.wal", ".wal", "000042.wal.tmp"} {
		if _, ok := ParseSegmentName(bad); ok {
			t.Fatalf("ParseSegmentName(%q) accepted", bad)
		}
	}
}

func TestCreateRefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), SegmentName(7))
	w, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := Create(path); err == nil {
		t.Fatal("Create over an existing segment should fail")
	}
}

// Tail tells a torn final write (safe to cut off) from damage that has
// good data after it (must not be cut off).
func TestCorruptTail(t *testing.T) {
	recs := randRecords(5, 300)
	data, offsets := writeRecords(t, recs)
	size := int64(len(data))
	corruptAt := func(off int64) *CorruptError {
		d := append([]byte(nil), data...)
		d[off] ^= 0xFF
		_, _, err := readAll(d)
		var ce *CorruptError
		if !errors.As(err, &ce) {
			t.Fatalf("no corruption detected for flip at %d: %v", off, err)
		}
		return ce
	}
	if ce := corruptAt(offsets[1] + HeaderSize + 1); ce.Tail(size) {
		t.Fatal("damage in a middle record must not count as a torn tail")
	}
	if ce := corruptAt(offsets[4] + HeaderSize + 1); !ce.Tail(size) {
		t.Fatal("damage in the last record should count as a torn tail")
	}
	_, _, err := readAll(data[:len(data)-2])
	var ce *CorruptError
	if !errors.As(err, &ce) || !ce.Tail(size-2) {
		t.Fatalf("short final record should be a torn tail: %v", err)
	}
	big := append([]byte(nil), data...)
	big[offsets[4]], big[offsets[4]+3] = 0xFF, 0x7F // absurd length in the last header
	_, _, err = readAll(big)
	if !errors.As(err, &ce) || ce.Tail(size) {
		t.Fatalf("an absurd length is damage, not a torn tail: %v", err)
	}
}
