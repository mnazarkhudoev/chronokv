// Package keys implements the internal key format used everywhere below the
// public API.
//
// Every write is stored under an "internal key" that glues the user key and
// the write's timestamp together:
//
//	escape(userKey) | 0x00 0x00 | bigEndian(^ts) | kind
//
// The point of this layout is that plain bytes.Compare gives exactly the
// order the engine needs: user keys ascending, and for one user key the
// newest version first (because the timestamp is inverted). So "what was k
// at time T" is a single seek to (k, T), and no part of the engine needs a
// custom comparator.
//
// The escaping (0x00 -> 0x00 0xFF) is there so user keys may contain zero
// bytes. Without it, "a" and "a\x00" would collide with the separator and
// the ordering would break for keys that are prefixes of each other.
package keys

import (
	"encoding/binary"
	"errors"
)

// Kind says whether an internal key is a value or a tombstone.
type Kind uint8

const (
	KindDelete Kind = 0
	KindSet    Kind = 1
)

// ErrMalformed is returned for byte strings that are not valid internal keys.
var ErrMalformed = errors.New("keys: malformed internal key")

const tsLen = 8 + 1 // inverted timestamp + kind

// escape appends the escaped form of userKey to dst.
func escape(dst, userKey []byte) []byte {
	for _, c := range userKey {
		if c == 0 {
			dst = append(dst, 0x00, 0xFF)
		} else {
			dst = append(dst, c)
		}
	}
	return dst
}

// UserKeyPrefix returns escape(userKey) followed by the separator. Every
// internal key for userKey starts with exactly these bytes, and comparing two
// prefixes gives the same answer as comparing the user keys, which is what
// makes it usable for scan bounds and as the bloom filter key.
func UserKeyPrefix(userKey []byte) []byte {
	dst := make([]byte, 0, len(userKey)+2)
	dst = escape(dst, userKey)
	return append(dst, 0x00, 0x00)
}

// Encode appends the internal key for (userKey, ts, kind) to dst.
func Encode(dst, userKey []byte, ts uint64, kind Kind) []byte {
	dst = escape(dst, userKey)
	dst = append(dst, 0x00, 0x00)
	dst = binary.BigEndian.AppendUint64(dst, ^ts)
	return append(dst, byte(kind))
}

// SeekKey returns the smallest internal key that is >= every version of
// userKey with a timestamp <= ts. Seeking to it lands on the newest version
// visible at ts (if there is one). It uses KindDelete because that is the
// smallest kind byte.
func SeekKey(userKey []byte, ts uint64) []byte {
	return Encode(nil, userKey, ts, KindDelete)
}

// SeekKeyPrefix is SeekKey for callers that already hold the escaped prefix.
func SeekKeyPrefix(prefix []byte, ts uint64) []byte {
	dst := make([]byte, 0, len(prefix)+tsLen)
	dst = append(dst, prefix...)
	dst = binary.BigEndian.AppendUint64(dst, ^ts)
	return append(dst, byte(KindDelete))
}

// prefixLen returns the length of the escaped user key plus separator, or -1.
func prefixLen(ik []byte) int {
	for i := 0; i < len(ik); i++ {
		if ik[i] != 0 {
			continue
		}
		if i+1 >= len(ik) {
			return -1
		}
		switch ik[i+1] {
		case 0x00:
			return i + 2
		case 0xFF:
			i++ // escaped zero byte, keep going
		default:
			return -1
		}
	}
	return -1
}

// Prefix returns the escaped user key plus separator part of ik, without
// copying. Two internal keys belong to the same user key exactly when their
// prefixes are equal. Returns nil if ik has no separator.
func Prefix(ik []byte) []byte {
	n := prefixLen(ik)
	if n < 0 {
		return nil
	}
	return ik[:n:n]
}

// Decode splits an internal key into its parts. The returned user key is a
// fresh, unescaped copy.
func Decode(ik []byte) (userKey []byte, ts uint64, kind Kind, err error) {
	n := prefixLen(ik)
	if n < 0 || len(ik)-n != tsLen {
		return nil, 0, 0, ErrMalformed
	}
	userKey = make([]byte, 0, n-2)
	for i := 0; i < n-2; i++ {
		userKey = append(userKey, ik[i])
		if ik[i] == 0 {
			i++ // skip the 0xFF escape byte
		}
	}
	ts = ^binary.BigEndian.Uint64(ik[n : n+8])
	kind = Kind(ik[n+8])
	if kind != KindSet && kind != KindDelete {
		return nil, 0, 0, ErrMalformed
	}
	return userKey, ts, kind, nil
}
