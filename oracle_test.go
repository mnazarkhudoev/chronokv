package chronokv

import (
	"bytes"
	"sort"
)

// oracle is the obviously-correct model the model test checks the DB
// against: a map from key to every version ever written, oldest first. It
// never forgets anything, so it can answer any AsOf question; the DB only
// has to agree with it inside the retention window.
type oracle struct {
	versions map[string][]Version
}

func newOracle() *oracle { return &oracle{versions: map[string][]Version{}} }

func (o *oracle) put(k string, v []byte, ts Timestamp) {
	o.versions[k] = append(o.versions[k], Version{TS: ts, Value: append([]byte{}, v...)})
}

func (o *oracle) del(k string, ts Timestamp) {
	o.versions[k] = append(o.versions[k], Version{TS: ts, Deleted: true})
}

// get returns the value of k at ts, or nil if it didn't exist then.
func (o *oracle) get(k string, ts uint64) []byte {
	vs := o.versions[k]
	i := sort.Search(len(vs), func(i int) bool { return uint64(vs[i].TS) > ts }) - 1
	if i < 0 || vs[i].Deleted {
		return nil
	}
	return vs[i].Value
}

type kv struct{ k, v string }

func (o *oracle) scan(start, end []byte, ts uint64) []kv {
	var out []kv
	for k := range o.versions {
		if start != nil && k < string(start) || end != nil && k >= string(end) {
			continue
		}
		if v := o.get(k, ts); v != nil {
			out = append(out, kv{k, string(v)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].k < out[j].k })
	return out
}

// history mirrors DB.History's contract.
func (o *oracle) history(k string, cutoff, visible uint64) []Version {
	vs := o.versions[k]
	var out []Version
	for i := len(vs) - 1; i >= 0; i-- {
		v := vs[i]
		if uint64(v.TS) > visible {
			continue
		}
		if uint64(v.TS) < cutoff {
			if !v.Deleted {
				out = append(out, v)
			}
			break
		}
		out = append(out, v)
	}
	return out
}

func sameVersions(a, b []Version) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].TS != b[i].TS || a[i].Deleted != b[i].Deleted || !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}
