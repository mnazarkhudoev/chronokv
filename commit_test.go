package chronokv

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

// A reader must never see half of a batch. The batch writes y before x, so
// if entries became visible one by one a reader could see the new y but the
// old x. With batch-level visibility x >= y always holds.
func TestBatchAtomicVisibility(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{SyncMode: SyncInterval})
	b := db.NewBatch()
	b.Put([]byte("y"), []byte("0"))
	b.Put([]byte("x"), []byte("0"))
	if _, err := db.Commit(b); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				yv, err1 := db.Get([]byte("y"))
				xv, err2 := db.Get([]byte("x"))
				if err1 != nil || err2 != nil {
					t.Errorf("get: %v %v", err1, err2)
					return
				}
				y, _ := strconv.Atoi(string(yv))
				x, _ := strconv.Atoi(string(xv))
				if x < y {
					t.Errorf("saw half a batch: x=%d y=%d", x, y)
					return
				}
			}
		}()
	}
	for i := 1; i <= 5000; i++ {
		b := db.NewBatch()
		v := []byte(strconv.Itoa(i))
		b.Put([]byte("y"), v)
		b.Put([]byte("x"), v)
		if _, err := db.Commit(b); err != nil {
			t.Fatal(err)
		}
	}
	close(done)
	wg.Wait()
}

func TestConcurrentCommits(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	const writers, each = 8, 500
	var mu sync.Mutex
	seen := map[Timestamp]bool{}
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				ts, err := db.Put([]byte(fmt.Sprintf("w%d-%d", w, i)), []byte("v"))
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if seen[ts] {
					t.Errorf("duplicate timestamp %d", ts)
				}
				seen[ts] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != writers*each {
		t.Fatalf("got %d distinct timestamps, want %d", len(seen), writers*each)
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < each; i++ {
			expectGet(t, db, fmt.Sprintf("w%d-%d", w, i), sp("v"))
		}
	}
}
