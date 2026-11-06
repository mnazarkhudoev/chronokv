package chronokv

import (
	"testing"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

func TestBatchEncodeDecode(t *testing.T) {
	b := &Batch{}
	b.Put([]byte(""), []byte("empty key"))
	b.Put([]byte("k"), []byte(""))
	b.Delete([]byte("gone"))
	b.Put([]byte("a\x00b"), []byte("zero byte key"))
	if b.Len() != 4 {
		t.Fatalf("Len = %d", b.Len())
	}
	enc := encodeBatch(nil, 12345, b)

	type op struct {
		kind keys.Kind
		k, v string
	}
	var got []op
	ts, err := decodeBatch(enc, func(kind keys.Kind, k, v []byte) {
		got = append(got, op{kind, string(k), string(v)})
	})
	if err != nil || ts != 12345 {
		t.Fatalf("decode: ts=%d err=%v", ts, err)
	}
	want := []op{
		{keys.KindSet, "", "empty key"},
		{keys.KindSet, "k", ""},
		{keys.KindDelete, "gone", ""},
		{keys.KindSet, "a\x00b", "zero byte key"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("op %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	for cut := 1; cut < len(enc); cut++ {
		if _, err := decodeBatch(enc[:cut], func(keys.Kind, []byte, []byte) {}); err == nil {
			t.Fatalf("truncated batch (%d/%d bytes) decoded without error", cut, len(enc))
		}
	}
}

func TestBatchCopiesAndResets(t *testing.T) {
	b := &Batch{}
	k := []byte("key")
	b.Put(k, k)
	k[0] = 'X'
	var seen string
	decodeBatch(encodeBatch(nil, 1, b), func(_ keys.Kind, k, _ []byte) { seen = string(k) })
	if seen != "key" {
		t.Fatalf("batch aliased caller's buffer: %q", seen)
	}
	b.Reset()
	if b.Len() != 0 {
		t.Fatal("Reset did not clear the batch")
	}
}

// Writing the same key twice in one batch: both writes share a timestamp,
// so only the last one may survive (otherwise two identical internal keys
// would end up in the memtable and in SSTables).
func TestBatchSameKeyTwice(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	b := db.NewBatch()
	b.Put([]byte("k"), []byte("first"))
	b.Put([]byte("k"), []byte("second"))
	b.Put([]byte("gone"), []byte("x"))
	b.Delete([]byte("gone"))
	b.Delete([]byte("back"))
	b.Put([]byte("back"), []byte("again"))
	if _, err := db.Commit(b); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		expectGet(t, db, "k", sp("second"))
		expectGet(t, db, "gone", nil)
		expectGet(t, db, "back", sp("again"))
	}
	check()
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	check()
	h, _ := db.History([]byte("k"), 0)
	if len(h) != 1 {
		t.Fatalf("History has %d versions, want 1", len(h))
	}
}
