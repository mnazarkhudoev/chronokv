package chronokv

import (
	"bytes"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

// Version is one entry in a key's history.
type Version struct {
	TS      Timestamp
	Value   []byte
	Deleted bool
}

// History returns the versions of key, newest first. limit <= 0 means all.
//
// It returns every version inside the retention window, plus the newest one
// before the window (which is the value the key had when the window starts)
// unless that one is a deletion. The result is the same whether or not
// compaction has already cleaned up older versions.
func (db *DB) History(key []byte, limit int) ([]Version, error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	visible := db.visibleTS.Load()
	cutoff := db.cutoff()
	rs := db.acquireReadState()
	defer rs.version.Unref()
	src := db.newInternalIter(rs)
	defer src.Close()

	prefix := keys.UserKeyPrefix(key)
	var out []Version
	for ok := src.SeekGE(keys.SeekKeyPrefix(prefix, visible)); ok && bytes.HasPrefix(src.Key(), prefix); ok = src.Next() {
		_, ts, kind, err := keys.Decode(src.Key())
		if err != nil {
			return nil, err
		}
		v := Version{TS: Timestamp(ts), Deleted: kind == keys.KindDelete}
		if !v.Deleted {
			v.Value = append([]byte{}, src.Value()...)
		}
		if ts < cutoff {
			if !v.Deleted {
				out = append(out, v)
			}
			break
		}
		out = append(out, v)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	if err := src.Error(); err != nil {
		return nil, wrapTableErr("history", err)
	}
	return out, nil
}
