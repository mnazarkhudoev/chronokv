package iterator

import (
	"bytes"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// Visible walks the user keys that exist at time asOf, within [start, end).
//
// The source holds every version of every key, newest first per key. For
// each user key Visible takes the first version with ts <= asOf. If that
// version is a value it is yielded; if it is a tombstone the key didn't
// exist at asOf and is skipped. Newer versions are jumped over with a seek
// rather than stepped through, so a key with a long history that mostly
// happened after asOf costs one seek, not thousands of Next calls.
type Visible struct {
	src         Iterator
	asOf        uint64
	start       []byte
	endPrefix   []byte // nil: unbounded
	started     bool
	done        bool
	lastPrefix  []byte // prefix of the key last yielded, still to be skipped
	pendingSkip bool
	key, value  []byte
	err         error
}

// NewVisible wraps src. start and end are user keys; nil means unbounded.
func NewVisible(src Iterator, asOf uint64, start, end []byte) *Visible {
	v := &Visible{src: src, asOf: asOf, start: start}
	if end != nil {
		v.endPrefix = keys.UserKeyPrefix(end)
	}
	return v
}

func (v *Visible) stop() bool {
	v.done = true
	if v.err == nil {
		v.err = v.src.Error()
	}
	return false
}

// Next advances to the next visible key.
func (v *Visible) Next() bool {
	if v.done {
		return false
	}
	switch {
	case !v.started:
		v.started = true
		if v.start != nil {
			v.src.SeekGE(keys.UserKeyPrefix(v.start))
		} else {
			v.src.First()
		}
	case v.pendingSkip:
		v.skipPrefix()
	}

	for {
		if !v.src.Valid() {
			return v.stop()
		}
		ik := v.src.Key()
		prefix := keys.Prefix(ik)
		if prefix == nil {
			v.err = keys.ErrMalformed
			return v.stop()
		}
		if v.endPrefix != nil && bytes.Compare(prefix, v.endPrefix) >= 0 {
			return v.stop()
		}
		uk, ts, kind, err := keys.Decode(ik)
		if err != nil {
			v.err = err
			return v.stop()
		}
		if ts > v.asOf {
			// Jump straight to the newest version that is old enough. This
			// lands either on that version or on the next user key.
			v.src.SeekGE(keys.SeekKeyPrefix(prefix, v.asOf))
			continue
		}
		v.lastPrefix = append(v.lastPrefix[:0], prefix...)
		if kind == keys.KindDelete {
			v.skipPrefix()
			continue
		}
		v.key, v.value = uk, v.src.Value()
		v.pendingSkip = true
		return true
	}
}

// skipPrefix steps past the remaining (older) versions of lastPrefix.
func (v *Visible) skipPrefix() {
	v.pendingSkip = false
	for v.src.Next() {
		if !bytes.HasPrefix(v.src.Key(), v.lastPrefix) {
			return
		}
	}
}

// Key is the current user key (unescaped). Valid until the next call to Next.
func (v *Visible) Key() []byte { return v.key }

// Value is the current value. Valid until the next call to Next.
func (v *Visible) Value() []byte { return v.value }

func (v *Visible) Error() error { return v.err }
func (v *Visible) Close() error { return v.src.Close() }
