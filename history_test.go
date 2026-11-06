package chronokv

import (
	"fmt"
	"testing"
	"time"
)

func fmtHistory(vs []Version) []string {
	var out []string
	for _, v := range vs {
		if v.Deleted {
			out = append(out, fmt.Sprintf("%d:DEL", v.TS))
		} else {
			out = append(out, fmt.Sprintf("%d:%s", v.TS, v.Value))
		}
	}
	return out
}

func TestHistory(t *testing.T) {
	db := openTest(t, t.TempDir(), Options{})
	t1 := mustPut(t, db, "k", "v1")
	db.Flush()
	t2 := mustDelete(t, db, "k")
	t3 := mustPut(t, db, "k", "v3")
	mustPut(t, db, "other", "x")

	h, err := db.History([]byte("k"), 0)
	if err != nil {
		t.Fatal(err)
	}
	eqStrings(t, fmtHistory(h), []string{fmt.Sprintf("%d:v3", t3), fmt.Sprintf("%d:DEL", t2), fmt.Sprintf("%d:v1", t1)})

	h, _ = db.History([]byte("k"), 2)
	eqStrings(t, fmtHistory(h), []string{fmt.Sprintf("%d:v3", t3), fmt.Sprintf("%d:DEL", t2)})

	h, _ = db.History([]byte("nope"), 0)
	if len(h) != 0 {
		t.Fatalf("history of a missing key = %v", h)
	}
}

func TestHistoryRetention(t *testing.T) {
	for _, deleteMiddle := range []bool{false, true} {
		t.Run(fmt.Sprint("deleteMiddle=", deleteMiddle), func(t *testing.T) {
			clock := newClock(time.Unix(0, 100))
			db := openTest(t, t.TempDir(), Options{Clock: clock.Now, Retention: 100})
			mustPut(t, db, "k", "a") // @100
			clock.Set(time.Unix(0, 200))
			if deleteMiddle {
				mustDelete(t, db, "k") // @200
			} else {
				mustPut(t, db, "k", "b") // @200
			}
			clock.Set(time.Unix(0, 300))
			mustPut(t, db, "k", "c") // @300
			clock.Set(time.Unix(0, 350)) // cutoff = 250

			h, err := db.History([]byte("k"), 0)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"300:c", "200:b"}
			if deleteMiddle {
				want = []string{"300:c"}
			}
			eqStrings(t, fmtHistory(h), want)
		})
	}
}
