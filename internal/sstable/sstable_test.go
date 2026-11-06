package sstable

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

type memFile struct{ *bytes.Reader }

func (memFile) Close() error { return nil }

type entry struct{ k, v []byte }

// testEntries returns 1000 user keys x 5 versions, newest first per key, in
// internal key order.
func testEntries() []entry {
	rng := rand.New(rand.NewPCG(3, 4))
	var es []entry
	for i := 0; i < 1000; i++ {
		uk := []byte(fmt.Sprintf("user/%06d", i))
		for v := 5; v >= 1; v-- {
			val := make([]byte, rng.IntN(301))
			for j := range val {
				val[j] = byte('a' + rng.IntN(26))
			}
			es = append(es, entry{keys.Encode(nil, uk, uint64(v*10), keys.KindSet), val})
		}
	}
	es[17].v = nil
	es[2500].v = bytes.Repeat([]byte("big"), 100<<10/3) // ~100KiB, bigger than a block
	return es
}

func build(t testing.TB, es []entry, opts WriterOptions) ([]byte, Props) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, opts)
	for _, e := range es {
		if err := w.Add(e.k, e.v); err != nil {
			t.Fatal(err)
		}
	}
	p, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if p.Size != int64(buf.Len()) {
		t.Fatalf("Props.Size = %d, wrote %d", p.Size, buf.Len())
	}
	return buf.Bytes(), p
}

func open(t testing.TB, data []byte) *Reader {
	t.Helper()
	r, err := Open(memFile{bytes.NewReader(data)}, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWriteReadAll(t *testing.T) {
	es := testEntries()
	data, props := build(t, es, WriterOptions{})
	if props.Entries != len(es) || !bytes.Equal(props.Smallest, es[0].k) || !bytes.Equal(props.Largest, es[len(es)-1].k) {
		t.Fatalf("bad props: %+v", props)
	}
	r := open(t, data)
	defer r.Close()
	it := r.NewIter()
	i := 0
	for ok := it.First(); ok; ok = it.Next() {
		if !bytes.Equal(it.Key(), es[i].k) || !bytes.Equal(it.Value(), es[i].v) {
			t.Fatalf("entry %d mismatch", i)
		}
		i++
	}
	if it.Error() != nil || i != len(es) {
		t.Fatalf("iterated %d/%d, err %v", i, len(es), it.Error())
	}
}

func TestSeekGE(t *testing.T) {
	es := testEntries()
	data, _ := build(t, es, WriterOptions{BlockSize: 512}) // small blocks: many boundaries
	r := open(t, data)
	it := r.NewIter()
	for i, e := range es {
		if !it.SeekGE(e.k) || !bytes.Equal(it.Key(), e.k) {
			t.Fatalf("SeekGE(entry %d) failed", i)
		}
		mid := append(append([]byte(nil), e.k...), 0)
		ok := it.SeekGE(mid)
		if i+1 < len(es) {
			if !ok || !bytes.Equal(it.Key(), es[i+1].k) {
				t.Fatalf("SeekGE(after entry %d) did not land on successor", i)
			}
		} else if ok {
			t.Fatal("SeekGE past the end should be invalid")
		}
	}
	if it.SeekGE([]byte("\xff\xff")) {
		t.Fatal("SeekGE beyond Largest should be invalid")
	}
	if !it.SeekGE(nil) || !bytes.Equal(it.Key(), es[0].k) {
		t.Fatal("SeekGE(nil) should land on the first entry")
	}
}

func TestBloomNoFalseNegatives(t *testing.T) {
	es := testEntries()
	data, _ := build(t, es, WriterOptions{})
	r := open(t, data)
	for _, e := range es {
		if !r.MayContain(keys.Prefix(e.k)) {
			t.Fatalf("bloom false negative for %q", e.k)
		}
	}
}

func TestBloomFalsePositiveRate(t *testing.T) {
	var es []entry
	for i := 0; i < 10000; i++ {
		es = append(es, entry{keys.Encode(nil, []byte(fmt.Sprintf("k%07d", i)), 1, keys.KindSet), nil})
	}
	data, _ := build(t, es, WriterOptions{BloomBitsPerKey: 10})
	r := open(t, data)
	fp := 0
	for i := 0; i < 10000; i++ {
		if r.MayContain(keys.UserKeyPrefix([]byte(fmt.Sprintf("absent-%07d", i)))) {
			fp++
		}
	}
	if rate := float64(fp) / 10000; rate >= 0.02 {
		t.Fatalf("false positive rate %.4f, want < 0.02", rate)
	}
}

func TestCorruptBlockDetected(t *testing.T) {
	data, _ := build(t, testEntries(), WriterOptions{})
	data[10] ^= 0xFF
	r := open(t, data) // footer and index are intact
	it := r.NewIter()
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		n++
	}
	if !errors.Is(it.Error(), ErrCorrupted) {
		t.Fatalf("after %d entries err = %v, want ErrCorrupted", n, it.Error())
	}
}

func TestCorruptFooter(t *testing.T) {
	data, _ := build(t, testEntries(), WriterOptions{})
	data[len(data)-8] ^= 0x01 // inside the magic number
	if _, err := Open(memFile{bytes.NewReader(data)}, int64(len(data))); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("Open err = %v, want ErrCorrupted", err)
	}
	if _, err := Open(memFile{bytes.NewReader(data[:10])}, 10); !errors.Is(err, ErrCorrupted) {
		t.Fatalf("Open(short) err = %v, want ErrCorrupted", err)
	}
}

func TestOutOfOrderAdd(t *testing.T) {
	w := NewWriter(&bytes.Buffer{}, WriterOptions{})
	if err := w.Add([]byte("b"), nil); err != nil {
		t.Fatal(err)
	}
	if err := w.Add([]byte("a"), nil); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("err = %v, want ErrOutOfOrder", err)
	}
	if err := w.Add([]byte("b"), nil); !errors.Is(err, ErrOutOfOrder) {
		t.Fatalf("duplicate err = %v, want ErrOutOfOrder", err)
	}
}

func TestEmptyTable(t *testing.T) {
	data, p := build(t, nil, WriterOptions{})
	if p.Entries != 0 {
		t.Fatal("expected 0 entries")
	}
	r := open(t, data)
	if r.NewIter().First() {
		t.Fatal("empty table iterator should be invalid")
	}
	if r.MayContain([]byte("x\x00\x00")) {
		t.Fatal("empty table should not contain anything")
	}
}

func TestEstimatedSizeGrows(t *testing.T) {
	w := NewWriter(&bytes.Buffer{}, WriterOptions{})
	prev := w.EstimatedSize()
	for _, e := range testEntries()[:500] {
		w.Add(e.k, e.v)
	}
	if w.EstimatedSize() <= prev {
		t.Fatal("EstimatedSize did not grow")
	}
}
