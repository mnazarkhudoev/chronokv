package chronokv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mnazarkhudoev/chronokv/internal/compaction"
)

// countEntries counts every stored version (memtables + tables), for tests.
func (db *DB) countEntries(t testing.TB) int {
	t.Helper()
	rs := db.acquireReadState()
	defer rs.version.Unref()
	it := db.newInternalIter(rs)
	defer it.Close()
	n := 0
	for ok := it.First(); ok; ok = it.Next() {
		n++
	}
	if err := it.Error(); err != nil {
		t.Fatal(err)
	}
	return n
}

func levelCounts(db *DB) []int {
	v := db.man.Current()
	defer v.Unref()
	var out []int
	for _, l := range v.Levels {
		out = append(out, len(l))
	}
	return out
}

func TestCompactPreservesAsOfReads(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{BlockSize: 512})
	exp := map[string][]expectation{}
	for round := 0; round < 5; round++ {
		for i := 0; i < 500; i++ {
			k := fmt.Sprintf("k%04d", i)
			var e expectation
			if (i+round)%11 == 0 {
				e.ts = mustDelete(t, db, k)
			} else {
				v := fmt.Sprintf("r%d", round)
				e.ts, e.want = mustPut(t, db, k, v), sp(v)
			}
			exp[k] = append(exp[k], e)
		}
		if err := db.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	checkRounds(t, db, exp)
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	nonEmpty := 0
	for _, n := range levelCounts(db) {
		if n > 0 {
			nonEmpty++
		}
	}
	if nonEmpty != 1 {
		t.Fatalf("after Compact data is spread over levels %v", levelCounts(db))
	}
	checkRounds(t, db, exp)
	if got, want := db.countEntries(t), 2500; got != want {
		t.Fatalf("Compact without retention dropped versions: %d entries, want %d", got, want)
	}
}

func TestCompactDropsExpiredHistory(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	clock := newClock(t0)
	db := openTest(t, t.TempDir(), Options{Clock: clock.Now, Retention: time.Hour})
	mustPut(t, db, "k", "v1")
	clock.Advance(10 * time.Minute)
	t2 := mustPut(t, db, "k", "v2")
	clock.Advance(2 * time.Hour)
	t3 := mustPut(t, db, "k", "v3")

	if n := db.countEntries(t); n != 3 {
		t.Fatalf("before compaction: %d entries", n)
	}
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	if n := db.countEntries(t); n != 2 {
		t.Fatalf("after compaction: %d entries, want 2 (v1 should be gone)", n)
	}
	h, _ := db.History([]byte("k"), 0)
	eqStrings(t, fmtHistory(h), []string{fmt.Sprintf("%d:v3", t3), fmt.Sprintf("%d:v2", t2)})
}

// compactInternal runs a full compaction the way the background goroutine
// does: without the Flush that Compact() starts with. (Flush goes through
// the commit pipeline, which bumps lastTS to the current clock and would
// hide the bug this test is about.)
func compactInternal(t testing.TB, db *DB) {
	t.Helper()
	db.compactMu.Lock()
	defer db.compactMu.Unlock()
	v := db.man.Current()
	c := compaction.All(v)
	v.Unref()
	if err := db.runCompaction(c); err != nil {
		t.Fatal(err)
	}
}

func TestBackwardClockAfterCompaction(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	clock := newClock(t0)
	dir := t.TempDir()
	db, err := Open(dir, Options{Clock: clock.Now, Retention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t1 := mustPut(t, db, "k", "v1")
	clock.Advance(10 * time.Minute)
	mustPut(t, db, "k", "v2")
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(5 * time.Hour) // no writes, so lastTS stays at t0+10m
	compactInternal(t, db)       // cutoff t0+4h10m: v1 is dropped
	if n := db.countEntries(t); n != 1 {
		t.Fatalf("expected v1 to be compacted away, %d entries left", n)
	}

	clock.Set(t0.Add(20 * time.Minute)) // clock jumps back
	check := func(db *DB) {
		t.Helper()
		if _, err := db.Get([]byte("k"), AsOf(t1+1)); !errors.Is(err, ErrHistoryTruncated) {
			t.Fatalf("read of compacted history err = %v, want ErrHistoryTruncated", err)
		}
		expectGet(t, db, "k", sp("v2"))
	}
	check(db)
	db.Close()
	db = openTest(t, dir, Options{Clock: clock.Now, Retention: time.Hour})
	check(db)
}

func sstNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, "*.sst"))
	out := map[string]bool{}
	for _, p := range m {
		out[filepath.Base(p)] = true
	}
	return out
}

func TestIteratorPinsFilesDuringCompaction(t *testing.T) {
	dir := t.TempDir()
	db := openTest(t, dir, Options{})
	for round := 0; round < 3; round++ {
		for i := 0; i < 100; i++ {
			mustPut(t, db, fmt.Sprintf("k%03d", i), fmt.Sprint(round))
		}
		db.Flush()
	}
	before := sstNames(t, dir)
	it := db.Scan(nil, nil)
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	for name := range before {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s deleted while an iterator still uses it", name)
		}
	}
	n := 0
	for it.Next() {
		if string(it.Value()) != "2" {
			t.Fatalf("%s = %s", it.Key(), it.Value())
		}
		n++
	}
	if it.Err() != nil || n != 100 {
		t.Fatalf("scan saw %d keys, err %v", n, it.Err())
	}
	it.Close()
	for name := range before {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still around after the iterator closed", name)
		}
	}
}

func TestBackgroundCompactionBoundsL0(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{MemtableSize: 16 << 10, SyncMode: SyncInterval})
	val := make([]byte, 1000)
	for i := 0; i < 4000; i++ {
		if _, err := db.Put([]byte(fmt.Sprintf("k%06d", i%1500)), val); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for levelCounts(db)[0] > 8 {
		if time.Now().After(deadline) {
			t.Fatalf("L0 never shrank: levels %v", levelCounts(db))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i := 0; i < 1500; i++ {
		if _, err := db.Get([]byte(fmt.Sprintf("k%06d", i))); err != nil {
			t.Fatalf("k%06d: %v", i, err)
		}
	}
}
