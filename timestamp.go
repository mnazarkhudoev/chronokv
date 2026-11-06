package chronokv

import "time"

// Timestamp identifies a point in the database's history. It is nanoseconds
// since the Unix epoch, assigned by the database at commit time. Timestamps
// are strictly increasing even if the system clock goes backwards.
type Timestamp uint64

// Time converts the timestamp to a time.Time.
func (t Timestamp) Time() time.Time { return time.Unix(0, int64(t)) }

type readOpts struct {
	asOf    uint64
	hasAsOf bool
}

// ReadOption modifies a read.
type ReadOption func(*readOpts)

// AsOf makes a read return the data as it was at ts. Timestamps in the
// future read the latest data; timestamps older than the retention window
// fail with ErrHistoryTruncated.
func AsOf(ts Timestamp) ReadOption {
	return func(o *readOpts) {
		o.asOf = uint64(ts)
		o.hasAsOf = true
	}
}

func collectReadOpts(opts []ReadOption) readOpts {
	var ro readOpts
	for _, o := range opts {
		o(&ro)
	}
	return ro
}
