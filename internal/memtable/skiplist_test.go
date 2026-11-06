package memtable

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"testing"
)

func TestOrderAndSeek(t *testing.T) {
	m := New()
	var keys []string
	for i := 0; i < 2000; i++ {
		keys = append(keys, fmt.Sprintf("key-%05d", i*3))
	}
	shuffled := append([]string(nil), keys...)
	rand.New(rand.NewPCG(7, 7)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	for _, k := range shuffled {
		m.Add([]byte(k), []byte("v"+k))
	}
	sort.Strings(keys)

	it := m.NewIter()
	i := 0
	for ok := it.First(); ok; ok = it.Next() {
		if string(it.Key()) != keys[i] || string(it.Value()) != "v"+keys[i] {
			t.Fatalf("pos %d: got %q=%q, want %q", i, it.Key(), it.Value(), keys[i])
		}
		i++
	}
	if i != len(keys) {
		t.Fatalf("iterated %d entries, want %d", i, len(keys))
	}

	for j, k := range keys {
		if !it.SeekGE([]byte(k)) || string(it.Key()) != k {
			t.Fatalf("SeekGE(%q) landed on %q", k, it.Key())
		}
		between := k + "\x00"
		ok := it.SeekGE([]byte(between))
		if j+1 < len(keys) {
			if !ok || string(it.Key()) != keys[j+1] {
				t.Fatalf("SeekGE(%q) landed on %q, want %q", between, it.Key(), keys[j+1])
			}
		} else if ok {
			t.Fatalf("SeekGE past the end returned %q", it.Key())
		}
	}
	if it.SeekGE([]byte("zzz")) || it.Valid() {
		t.Fatal("SeekGE beyond max should be invalid")
	}
	if !it.SeekGE(nil) || string(it.Key()) != keys[0] {
		t.Fatal("SeekGE(nil) should land on the first key")
	}
}

func TestAddCopiesInputs(t *testing.T) {
	m := New()
	k, v := []byte("k"), []byte("v")
	m.Add(k, v)
	k[0], v[0] = 'x', 'x'
	it := m.NewIter()
	if !it.First() || string(it.Key()) != "k" || string(it.Value()) != "v" {
		t.Fatalf("memtable aliased caller buffers: %q=%q", it.Key(), it.Value())
	}
}

func TestConcurrentReaders(t *testing.T) {
	m := New()
	const n = 20000
	var wg sync.WaitGroup
	done := make(chan struct{})
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				it := m.NewIter()
				var prev []byte
				for ok := it.First(); ok; ok = it.Next() {
					if prev != nil && bytes.Compare(prev, it.Key()) >= 0 {
						t.Errorf("out of order: %q then %q", prev, it.Key())
						return
					}
					if !bytes.Equal(it.Key(), it.Value()) {
						t.Errorf("torn entry %q=%q", it.Key(), it.Value())
						return
					}
					prev = it.Key()
				}
			}
		}()
	}
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("%08d", i))
		m.Add(k, k)
	}
	close(done)
	wg.Wait()
}

func TestApproxSizeGrows(t *testing.T) {
	m := New()
	if !m.Empty() || m.ApproxSize() != 0 {
		t.Fatal("new memtable should be empty")
	}
	prev := m.ApproxSize()
	for i := 0; i < 100; i++ {
		m.Add([]byte(fmt.Sprint(i)), nil)
		if m.Empty() {
			t.Fatal("Empty() after Add")
		}
		if s := m.ApproxSize(); s <= prev {
			t.Fatalf("size did not grow: %d -> %d", prev, s)
		} else {
			prev = s
		}
	}
}
