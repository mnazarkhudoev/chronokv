// Package compaction decides which tables to merge (Pick, All) and how
// versions are thinned out while merging (Merge). It does no I/O itself;
// the DB feeds it iterators and writes whatever Merge emits.
package compaction

import (
	"bytes"

	"github.com/mnazarkhudoev/chronokv/internal/iterator"
	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// Merge streams src (internal keys, newest version first per user key) to
// emit, dropping versions that no read inside the retention window can see.
//
// The rule per user key:
//   - every version with ts >= cutoff is kept;
//   - the newest version with ts < cutoff is kept too, because a read at the
//     cutoff (or anywhere before the next version) needs it as its answer;
//   - everything older than that is dropped.
//
// One exception: if that newest-old version is a tombstone and nothing
// older can exist below this compaction (bottommost), the tombstone itself
// can go. A read would see "deleted" either way.
func Merge(src iterator.Iterator, cutoff uint64, bottommost bool, emit func(ik, value []byte) error) error {
	var (
		prev      []byte
		curPrefix []byte
		seenOld   bool
	)
	for ok := src.First(); ok; ok = src.Next() {
		ik := src.Key()
		if prev != nil && bytes.Equal(ik, prev) {
			continue // same entry in two inputs
		}
		prev = append(prev[:0], ik...)

		_, ts, kind, err := keys.Decode(ik)
		if err != nil {
			return err
		}
		if p := keys.Prefix(ik); !bytes.Equal(p, curPrefix) {
			curPrefix = append(curPrefix[:0], p...)
			seenOld = false
		}

		switch {
		case ts >= cutoff:
		case !seenOld:
			seenOld = true
			if kind == keys.KindDelete && bottommost {
				continue
			}
		default:
			continue
		}
		if err := emit(ik, src.Value()); err != nil {
			return err
		}
	}
	return src.Error()
}
