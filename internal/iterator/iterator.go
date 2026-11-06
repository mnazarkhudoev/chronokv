// Package iterator has the two iterators that turn a pile of sorted sources
// (memtables and SSTables) into answers: a merging iterator that presents
// them as one sorted stream, and a visibility iterator that picks, for each
// user key, the version a reader at time T should see.
package iterator

// Iterator is the common shape of memtable, sstable and merging iterators.
// Keys are internal keys (see package keys). Key and Value are only valid
// until the next positioning call.
type Iterator interface {
	First() bool
	SeekGE(key []byte) bool
	Next() bool
	Valid() bool
	Key() []byte
	Value() []byte
	Error() error
	Close() error
}
