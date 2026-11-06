package chronokv

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func scanAll(t testing.TB, db *DB, start, end []byte, opts ...ReadOption) []string {
	t.Helper()
	it := db.Scan(start, end, opts...)
	defer it.Close()
	var out []string
	for it.Next() {
		out = append(out, fmt.Sprintf("%s=%s", it.Key(), it.Value()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func eqStrings(t testing.TB, got, want []string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestScanAsOf(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	letters := "abcdefghijklmnopqrstuvwxyz"
	for _, c := range letters {
		mustPut(t, db, string(c), "v1")
	}
	t1 := db.Now()
	db.Flush()
	for i, c := range letters {
		if i%2 == 0 {
			mustPut(t, db, string(c), "v2")
		}
	}
	t2 := db.Now()
	db.Flush()
	for i, c := range letters {
		if i%3 == 0 {
			mustDelete(t, db, string(c))
		}
	}

	var want1, want2, wantLatest []string
	for i, c := range letters {
		want1 = append(want1, string(c)+"=v1")
		v := "v1"
		if i%2 == 0 {
			v = "v2"
		}
		want2 = append(want2, string(c)+"="+v)
		if i%3 != 0 {
			wantLatest = append(wantLatest, string(c)+"="+v)
		}
	}
	eqStrings(t, scanAll(t, db, nil, nil, AsOf(t1)), want1)
	eqStrings(t, scanAll(t, db, nil, nil, AsOf(t2)), want2)
	eqStrings(t, scanAll(t, db, nil, nil), wantLatest)
	eqStrings(t, scanAll(t, db, []byte("c"), []byte("f"), AsOf(t1)), []string{"c=v1", "d=v1", "e=v1"})
}

func TestScanBoundsWithZeroBytes(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	for _, k := range []string{"a", "a\x00", "a\x00\x01", "ab", "b"} {
		mustPut(t, db, k, "x")
	}
	db.Flush()
	got := scanAll(t, db, []byte("a\x00"), []byte("ab"))
	eqStrings(t, got, []string{"a\x00=x", "a\x00\x01=x"})
}

func TestScanSnapshotIsolation(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	for i := 0; i < 50; i++ {
		mustPut(t, db, fmt.Sprintf("old-%02d", i), "v")
	}
	it := db.Scan(nil, nil)
	defer it.Close()
	for i := 0; i < 100; i++ {
		mustPut(t, db, fmt.Sprintf("new-%02d", i), "v")
	}
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	n := 0
	for it.Next() {
		if string(it.Key()[:4]) != "old-" {
			t.Fatalf("scan saw a key written after it started: %q", it.Key())
		}
		n++
	}
	if it.Err() != nil || n != 50 {
		t.Fatalf("saw %d keys, err %v", n, it.Err())
	}
}

func TestScanTruncated(t *testing.T) {
	clock := newClock(time.Unix(1_000_000, 0))
	db := openTest(t, t.TempDir(), Options{Clock: clock.Now, Retention: time.Hour})
	ts := mustPut(t, db, "k", "v")
	clock.Advance(2 * time.Hour)
	it := db.Scan(nil, nil, AsOf(ts))
	defer it.Close()
	if it.Next() || !errors.Is(it.Err(), ErrHistoryTruncated) {
		t.Fatalf("Next/Err = %v, want ErrHistoryTruncated", it.Err())
	}
}

func TestScanAfterClose(t *testing.T) {
	db, _ := Open(t.TempDir(), Options{})
	db.Close()
	it := db.Scan(nil, nil)
	if it.Next() || !errors.Is(it.Err(), ErrClosed) {
		t.Fatalf("Err = %v, want ErrClosed", it.Err())
	}
	it.Close()
}
