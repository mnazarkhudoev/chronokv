package chronokv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPutGetDelete(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	ts1 := mustPut(t, db, "a", "1")
	expectGet(t, db, "a", sp("1"))
	mustDelete(t, db, "a")
	expectGet(t, db, "a", nil)
	expectGet(t, db, "a", sp("1"), AsOf(ts1))
	expectGet(t, db, "never", nil)
}

func TestEmptyValueDistinctFromMissing(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	mustPut(t, db, "k", "")
	v, err := db.Get([]byte("k"))
	if err != nil || len(v) != 0 {
		t.Fatalf("Get(k) = %q, %v; want empty value, nil", v, err)
	}
	if _, err := db.Get([]byte("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(nope) err = %v, want ErrNotFound", err)
	}
}

func TestTimestampsMonotonicWithBackwardClock(t *testing.T) {
	clock := newClock(time.Unix(0, 1000))
	db := openTest(t, t.TempDir(), Options{Clock: clock.Now})
	if ts := mustPut(t, db, "a", "1"); ts != 1000 {
		t.Fatalf("ts1 = %d, want 1000", ts)
	}
	clock.Set(time.Unix(0, 500))
	if ts := mustPut(t, db, "a", "2"); ts != 1001 {
		t.Fatalf("ts2 = %d, want 1001", ts)
	}
}

func TestAsOfEdges(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	clock := newClock(t0)
	db := openTest(t, t.TempDir(), Options{Clock: clock.Now, Retention: time.Hour})
	ts := mustPut(t, db, "k", "v")

	expectGet(t, db, "k", sp("v"), AsOf(Timestamp(t0.Add(time.Hour).UnixNano()))) // future: clamped to latest
	expectGet(t, db, "k", nil, AsOf(ts-1))                                        // before the first write

	clock.Advance(2 * time.Hour)
	cut := db.cutoff()
	if want := uint64(t0.Add(time.Hour).UnixNano()); cut != want {
		t.Fatalf("cutoff = %d, want %d", cut, want)
	}
	expectGet(t, db, "k", sp("v"), AsOf(Timestamp(cut)))
	if _, err := db.Get([]byte("k"), AsOf(Timestamp(cut-1))); !errors.Is(err, ErrHistoryTruncated) {
		t.Fatalf("Get before cutoff err = %v, want ErrHistoryTruncated", err)
	}
}

func TestNowStable(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	now := db.Now()
	ts := mustPut(t, db, "a", "new")
	if ts <= now {
		t.Fatalf("Put ts %d not after Now %d", ts, now)
	}
	expectGet(t, db, "a", nil, AsOf(now))
}

func TestReopenRecoversFromWAL(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var last Timestamp
	for i := 0; i < 100; i++ {
		last = mustPut(t, db, fmt.Sprint(i), fmt.Sprint("v", i))
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTest(t, dir, Options{})
	for i := 0; i < 100; i++ {
		expectGet(t, db, fmt.Sprint(i), sp(fmt.Sprint("v", i)))
	}
	if db.Now() < last {
		t.Fatalf("Now() = %d after reopen, last write was %d", db.Now(), last)
	}
	if ts := mustPut(t, db, "after", "x"); ts <= last {
		t.Fatalf("timestamp went backwards after reopen: %d <= %d", ts, last)
	}
}

func walFiles(t *testing.T, dir string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.wal"))
	return m
}

func TestTornWALTailIgnored(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		mustPut(t, db, fmt.Sprint(i), "v")
	}
	db.Close()
	ws := walFiles(t, dir)
	last := ws[len(ws)-1]
	st, _ := os.Stat(last)
	if err := os.Truncate(last, st.Size()-3); err != nil {
		t.Fatal(err)
	}

	db, err = Open(dir, Options{})
	if err != nil {
		t.Fatalf("Open after torn tail: %v", err)
	}
	for i := 0; i < 9; i++ {
		expectGet(t, db, fmt.Sprint(i), sp("v"))
	}
	mustPut(t, db, "new", "x")
	db.Close()
	db = openTest(t, dir, Options{})
	expectGet(t, db, "new", sp("x"))
	expectGet(t, db, "0", sp("v"))
}

func TestDirLocked(t *testing.T) {
	dir := t.TempDir()
	openTest(t, dir, Options{})
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("second Open err = %v, want ErrLocked", err)
	}
}

func TestUseAfterClose(t *testing.T) {
	db, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := db.Put([]byte("a"), nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("Put err = %v", err)
	}
	if _, err := db.Get([]byte("a")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get err = %v", err)
	}
	if _, err := db.Commit(db.NewBatch()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Commit err = %v", err)
	}
	if err := db.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close err = %v", err)
	}
}

func TestBatchTooLarge(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	b := db.NewBatch()
	b.Put([]byte("big"), make([]byte, 33<<20))
	if _, err := db.Commit(b); !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("err = %v, want ErrBatchTooLarge", err)
	}
}
