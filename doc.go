// Package chronokv is an embeddable key-value store that remembers.
//
// Every write gets a timestamp and old versions are kept for a configurable
// retention window, so besides the usual Get/Put/Delete you can ask what a
// key, or a whole key range, looked like at any moment inside that window:
//
//	db, _ := chronokv.Open("./data", chronokv.Options{Retention: 24 * time.Hour})
//	ts, _ := db.Put([]byte("user:42"), []byte(`{"plan":"free"}`))
//	db.Put([]byte("user:42"), []byte(`{"plan":"pro"}`))
//
//	v, _ := db.Get([]byte("user:42"))              // {"plan":"pro"}
//	v, _ = db.Get([]byte("user:42"), chronokv.AsOf(ts)) // {"plan":"free"}
//
// Under the hood it is a log-structured merge tree: a write-ahead log, an
// in-memory skiplist, immutable sorted tables on disk and background
// compaction. Versions are part of the key, so time travel is a seek, not a
// separate history table. See docs/design.md for the details.
package chronokv
