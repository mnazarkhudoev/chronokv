package chronokv

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeClock lets tests control time, including moving it backwards.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(start time.Time) *fakeClock { return &fakeClock{t: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// openTest opens a DB in dir and closes it when the test ends.
func openTest(t testing.TB, dir string, opts Options) *DB {
	t.Helper()
	db, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil && !errors.Is(err, ErrClosed) {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func mustPut(t testing.TB, db *DB, k, v string) Timestamp {
	t.Helper()
	ts, err := db.Put([]byte(k), []byte(v))
	if err != nil {
		t.Fatalf("Put(%q): %v", k, err)
	}
	return ts
}

func mustDelete(t testing.TB, db *DB, k string) Timestamp {
	t.Helper()
	ts, err := db.Delete([]byte(k))
	if err != nil {
		t.Fatalf("Delete(%q): %v", k, err)
	}
	return ts
}

// expectGet checks Get(k, opts...) returns want, or ErrNotFound if want is nil.
func expectGet(t testing.TB, db *DB, k string, want *string, opts ...ReadOption) {
	t.Helper()
	got, err := db.Get([]byte(k), opts...)
	if want == nil {
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) = %q, %v; want ErrNotFound", k, got, err)
		}
		return
	}
	if err != nil || string(got) != *want {
		t.Fatalf("Get(%q) = %q, %v; want %q", k, got, err, *want)
	}
}

func sp(s string) *string { return &s }
