package keys

import (
	"bytes"
	"cmp"
	"testing"
)

func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte("a\x00b"), uint64(7))
	f.Add([]byte{}, uint64(0))
	f.Add([]byte{0, 0, 0xff}, ^uint64(0))
	f.Fuzz(func(t *testing.T, key []byte, ts uint64) {
		ik := Encode(nil, key, ts, KindSet)
		k, gotTS, kind, err := Decode(ik)
		if err != nil || !bytes.Equal(k, key) || gotTS != ts || kind != KindSet {
			t.Fatalf("round trip failed for %x@%d: %x@%d kind=%d err=%v", key, ts, k, gotTS, kind, err)
		}
		if !bytes.Equal(Prefix(ik), UserKeyPrefix(key)) {
			t.Fatalf("Prefix mismatch for %x", key)
		}
	})
}

func FuzzOrdering(f *testing.F) {
	f.Add([]byte("a"), []byte("ab"), uint64(1), uint64(2))
	f.Add([]byte("a\x00"), []byte("a"), uint64(5), uint64(5))
	f.Add([]byte{0}, []byte{0, 0}, uint64(0), ^uint64(0))
	f.Fuzz(func(t *testing.T, k1, k2 []byte, t1, t2 uint64) {
		got := bytes.Compare(Encode(nil, k1, t1, KindSet), Encode(nil, k2, t2, KindSet))
		want := bytes.Compare(k1, k2)
		if want == 0 {
			want = cmp.Compare(t2, t1) // newer first
		}
		if got != want {
			t.Fatalf("order(%x@%d, %x@%d) = %d, want %d", k1, t1, k2, t2, got, want)
		}
		// Prefix comparison must agree with user key comparison too; scan bounds rely on it.
		if pc, kc := bytes.Compare(UserKeyPrefix(k1), UserKeyPrefix(k2)), bytes.Compare(k1, k2); pc != kc {
			t.Fatalf("prefix order(%x,%x) = %d, want %d", k1, k2, pc, kc)
		}
	})
}
