package keys

import (
	"bytes"
	"errors"
	"math"
	"sort"
	"testing"
)

var testUserKeys = []string{"", "a", "a\x00", "\x00\x00", "\xff", "ab", "a\x00\xff"}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	for _, k := range testUserKeys {
		for _, ts := range []uint64{0, 1, 42, math.MaxUint64} {
			for _, kind := range []Kind{KindSet, KindDelete} {
				ik := Encode(nil, []byte(k), ts, kind)
				gotK, gotTS, gotKind, err := Decode(ik)
				if err != nil {
					t.Fatalf("Decode(%q@%d): %v", k, ts, err)
				}
				if string(gotK) != k || gotTS != ts || gotKind != kind {
					t.Fatalf("round trip %q@%d/%d: got %q@%d/%d", k, ts, kind, gotK, gotTS, gotKind)
				}
				if !bytes.Equal(Prefix(ik), UserKeyPrefix([]byte(k))) {
					t.Fatalf("Prefix(%q) = %x, want %x", k, Prefix(ik), UserKeyPrefix([]byte(k)))
				}
			}
		}
	}
}

func TestEncodeAppends(t *testing.T) {
	dst := []byte("xx")
	ik := Encode(dst, []byte("k"), 1, KindSet)
	if !bytes.HasPrefix(ik, []byte("xx")) {
		t.Fatalf("Encode did not append to dst: %x", ik)
	}
}

func TestOrdering(t *testing.T) {
	type kv struct {
		k  string
		ts uint64
	}
	ordered := []kv{
		{"", 9},
		{"a", 5},
		{"a", 3},
		{"a\x00", 9},
		{"a\x00\x01", 1},
		{"ab", 1},
		{"b", math.MaxUint64},
	}
	for i := 1; i < len(ordered); i++ {
		a := Encode(nil, []byte(ordered[i-1].k), ordered[i-1].ts, KindSet)
		b := Encode(nil, []byte(ordered[i].k), ordered[i].ts, KindSet)
		if bytes.Compare(a, b) >= 0 {
			t.Fatalf("%q@%d should sort before %q@%d", ordered[i-1].k, ordered[i-1].ts, ordered[i].k, ordered[i].ts)
		}
	}
}

func TestSeekKeyFindsNewestAtOrBefore(t *testing.T) {
	entries := [][]byte{
		Encode(nil, []byte("k"), 30, KindSet),
		Encode(nil, []byte("k"), 20, KindDelete),
		Encode(nil, []byte("k"), 10, KindSet),
		Encode(nil, []byte("ka"), 99, KindSet),
	}
	if !sort.SliceIsSorted(entries, func(i, j int) bool { return bytes.Compare(entries[i], entries[j]) < 0 }) {
		t.Fatal("test entries not sorted")
	}
	seek := func(target []byte) int {
		return sort.Search(len(entries), func(i int) bool { return bytes.Compare(entries[i], target) >= 0 })
	}
	if got := seek(SeekKey([]byte("k"), 25)); got != 1 {
		t.Fatalf("SeekKey(k,25) landed at %d, want 1 (ts=20)", got)
	}
	if got := seek(SeekKey([]byte("k"), 30)); got != 0 {
		t.Fatalf("SeekKey(k,30) landed at %d, want 0 (ts=30)", got)
	}
	if got := seek(SeekKey([]byte("k"), 5)); got != 3 {
		t.Fatalf("SeekKey(k,5) landed at %d, want 3 (next user key)", got)
	}
	if !bytes.Equal(SeekKeyPrefix(UserKeyPrefix([]byte("k")), 25), SeekKey([]byte("k"), 25)) {
		t.Fatal("SeekKeyPrefix and SeekKey disagree")
	}
}

func TestDecodeMalformed(t *testing.T) {
	for _, in := range []string{"", "abc", "a\x00\x00", "a\x00\x00" + "1234567", "a\x00"} {
		if _, _, _, err := Decode([]byte(in)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Decode(%q) err = %v, want ErrMalformed", in, err)
		}
		if p := Prefix([]byte(in)); in != "a\x00\x00" && in != "a\x00\x00"+"1234567" && p != nil {
			t.Fatalf("Prefix(%q) = %x, want nil", in, p)
		}
	}
}
