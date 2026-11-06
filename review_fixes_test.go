package chronokv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// C1: one damaged byte in the manifest must fail Open, not silently start
// from an empty file set and delete every table.
func TestCorruptManifestFailsOpen(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(dir, Options{})
	mustPut(t, db, "a", "1")
	db.Flush()
	db.Close()
	db, _ = Open(dir, Options{})
	mustPut(t, db, "b", "2")
	db.Flush()
	db.Close()

	cur, _ := os.ReadFile(filepath.Join(dir, "CURRENT"))
	path := filepath.Join(dir, strings.TrimSpace(string(cur)))
	data, _ := os.ReadFile(path)
	data[20] ^= 0xFF
	os.WriteFile(path, data, 0o644)
	before := countFiles(t, dir, "*.sst")

	if db, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupted) {
		if db != nil {
			db.Close()
		}
		t.Fatalf("Open with a corrupt manifest: err = %v, want ErrCorrupted", err)
	}
	if after := countFiles(t, dir, "*.sst"); after != before {
		t.Fatalf("tables deleted: %d before, %d after", before, after)
	}
}

// C2: a Version released after Close (here by a leaked iterator) must not
// run cleanup against a directory that another instance now owns.
func TestReleaseAfterCloseKeepsFiles(t *testing.T) {
	dir := t.TempDir()
	db1, _ := Open(dir, Options{})
	mustPut(t, db1, "a", "1")
	db1.Flush()
	it := db1.Scan(nil, nil)
	mustPut(t, db1, "b", "2")
	db1.Flush()
	db1.Close()

	db2 := openTest(t, dir, Options{})
	mustPut(t, db2, "c", "3")
	if err := db2.Flush(); err != nil {
		t.Fatal(err)
	}
	it.Close() // releases db1's last Version
	expectGet(t, db2, "c", sp("3"))
	expectGet(t, db2, "a", sp("1"))
}

// C2, second path: user Flush/Compact overlapping Close must not keep
// touching the directory after Close returns.
func TestCompactRacingClose(t *testing.T) {
	dir := t.TempDir()
	for round := 0; round < 30; round++ {
		db, err := Open(dir, Options{MemtableSize: 8 << 10})
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		for i := 0; i < 200; i++ {
			mustPut(t, db, fmt.Sprintf("r%02d-%03d", round, i), "v")
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < 5; i++ {
				db.Compact()
				db.Flush()
			}
		}()
		time.Sleep(time.Duration(round%5) * time.Millisecond)
		db.Close()
		<-done
	}
	db := openTest(t, dir, Options{})
	for round := 0; round < 30; round++ {
		for i := 0; i < 200; i += 37 {
			expectGet(t, db, fmt.Sprintf("r%02d-%03d", round, i), sp("v"))
		}
	}
}

// I1: damage in the middle of the newest WAL segment is corruption, not a
// torn tail; acknowledged writes must not be silently cut off.
func TestWALMidCorruptionFails(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(dir, Options{})
	for i := 0; i < 3; i++ {
		mustPut(t, db, fmt.Sprint(i), "value")
	}
	db.Close()
	ws := walFiles(t, dir)
	path := ws[len(ws)-1]
	data, _ := os.ReadFile(path)
	data[20] ^= 0xFF // inside the first record
	os.WriteFile(path, data, 0o644)
	size := int64(len(data))

	if db, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupted) {
		if db != nil {
			db.Close()
		}
		t.Fatalf("err = %v, want ErrCorrupted", err)
	}
	if st, _ := os.Stat(path); st.Size() != size {
		t.Fatalf("segment was truncated from %d to %d bytes", size, st.Size())
	}
}

// I2: in the error state reads keep working, so Now must still return a
// usable timestamp rather than 0.
func TestNowInErrorState(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	ts := mustPut(t, db, "a", "1")
	db.setBgErr(errors.New("disk on fire"))
	now := db.Now()
	if now < ts {
		t.Fatalf("Now() = %d in error state, last write was %d", now, ts)
	}
	expectGet(t, db, "a", sp("1"), AsOf(now))
}

// M1 (upgraded): a compaction landing between the cutoff check and pinning
// the read state must not turn a historical read into a wrong "not found".
func TestCompactionBetweenCutoffCheckAndRead(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	clock := newClock(t0)
	db := openTest(t, t.TempDir(), Options{Clock: clock.Now, Retention: time.Hour})
	mustPut(t, db, "k", "v1")
	clock.Advance(10 * time.Minute)
	mustPut(t, db, "k", "v2")
	db.Flush()
	clock.Set(t0.Add(64 * time.Minute)) // cutoff t0+4m: a read at t0+5m is allowed

	testHookGet = func() {
		testHookGet = nil
		clock.Advance(5 * time.Hour)
		compactInternal(t, db) // drops v1
	}
	defer func() { testHookGet = nil }()
	got, err := db.Get([]byte("k"), AsOf(Timestamp(t0.Add(5*time.Minute).UnixNano())))
	if !errors.Is(err, ErrHistoryTruncated) && string(got) != "v1" {
		t.Fatalf("Get = %q, %v; want v1 or ErrHistoryTruncated", got, err)
	}
}
