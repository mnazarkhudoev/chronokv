package chronokv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func countFiles(t testing.TB, dir, pattern string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

// history writes 3 rounds over n keys (values, some empty, some deletes) and
// returns, for every round, the timestamp and expected value of each key.
type expectation struct {
	ts   Timestamp
	want *string
}

func writeRounds(t testing.TB, db *DB, n int) map[string][]expectation {
	t.Helper()
	exp := map[string][]expectation{}
	for round := 1; round <= 3; round++ {
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("key-%05d", i)
			var e expectation
			switch {
			case round == 3 && i%7 == 0:
				e.ts = mustDelete(t, db, k)
			case round == 2 && i%5 == 0:
				e.ts, e.want = mustPut(t, db, k, ""), sp("")
			default:
				v := fmt.Sprintf("r%d-%d", round, i)
				e.ts, e.want = mustPut(t, db, k, v), sp(v)
			}
			exp[k] = append(exp[k], e)
		}
	}
	return exp
}

func checkRounds(t testing.TB, db *DB, exp map[string][]expectation) {
	t.Helper()
	for k, es := range exp {
		for _, e := range es {
			expectGet(t, db, k, e.want, AsOf(e.ts))
		}
		expectGet(t, db, k, es[len(es)-1].want)
	}
}

func TestFlushThenGet(t *testing.T) {
	dir := t.TempDir()
	db := openTest(t, dir, Options{})
	exp := writeRounds(t, db, 1000)
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, dir, "*.sst"); n < 1 {
		t.Fatalf("no sstables after Flush")
	}
	if n := countFiles(t, dir, "*.wal"); n != 1 {
		t.Fatalf("%d WAL segments after Flush, want only the active one", n)
	}
	checkRounds(t, db, exp)
	expectGet(t, db, "missing", nil)
}

func TestFlushEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	db := openTest(t, dir, Options{})
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, dir, "*.sst"); n != 0 {
		t.Fatalf("Flush of an empty memtable wrote %d tables", n)
	}
}

func TestAutoFlushSmallMemtable(t *testing.T) {
	dir := t.TempDir()
	db := openTest(t, dir, Options{MemtableSize: 16 << 10, SyncMode: SyncInterval})
	val := make([]byte, 1024)
	start := time.Now()
	for i := 0; i < 2048; i++ {
		copy(val, fmt.Sprintf("value-%d", i))
		if _, err := db.Put([]byte(fmt.Sprintf("k%06d", i)), val); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("writes took %v", d)
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, dir, "*.sst"); n < 10 {
		t.Fatalf("only %d sstables; memtable rotation isn't happening", n)
	}
	for i := 0; i < 2048; i++ {
		v, err := db.Get([]byte(fmt.Sprintf("k%06d", i)))
		if err != nil || string(v[:len(fmt.Sprintf("value-%d", i))]) != fmt.Sprintf("value-%d", i) {
			t.Fatalf("k%06d: %v", i, err)
		}
	}
}

func TestReopenAfterFlush(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	exp := writeRounds(t, db, 300)
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	for k, e := range writeRounds(t, db, 150) { // overwrite half, only in the WAL
		exp[k] = append(exp[k], e...)
	}
	db.Close()
	db = openTest(t, dir, Options{})
	checkRounds(t, db, exp)
}

func TestOrphanFilesRemovedOnOpen(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(dir, Options{})
	mustPut(t, db, "a", "1")
	db.Flush()
	db.Close()
	for _, name := range []string{"999999.sst", "000123.sst.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db = openTest(t, dir, Options{})
	for _, name := range []string{"999999.sst", "000123.sst.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists after Open", name)
		}
	}
	expectGet(t, db, "a", sp("1"))
}

func TestCorruptOlderWALSegmentFails(t *testing.T) {
	dir := t.TempDir()
	db, _ := Open(dir, Options{})
	for i := 0; i < 20; i++ {
		mustPut(t, db, fmt.Sprint(i), "first session")
	}
	db.Close()
	db, _ = Open(dir, Options{}) // replays segment A, starts segment B; A is still unflushed
	mustPut(t, db, "x", "second session")
	db.Close()

	m, _ := filepath.Glob(filepath.Join(dir, "*.wal"))
	if len(m) < 2 {
		t.Fatalf("expected 2 WAL segments, have %v", m)
	}
	data, _ := os.ReadFile(m[0])
	data[len(data)/2] ^= 0xFF
	os.WriteFile(m[0], data, 0o644)

	if db, err := Open(dir, Options{}); !errors.Is(err, ErrCorrupted) {
		if db != nil {
			db.Close()
		}
		t.Fatalf("Open err = %v, want ErrCorrupted", err)
	}
}
