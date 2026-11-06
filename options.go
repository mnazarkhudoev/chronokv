package chronokv

import (
	"log/slog"
	"time"

	"github.com/mnazarkhudoev/chronokv/internal/compaction"
)

// SyncMode controls when the write-ahead log is fsynced.
type SyncMode int

const (
	// SyncAlways fsyncs before a commit returns. Concurrent commits share
	// one fsync (group commit), so this is less slow than it sounds.
	SyncAlways SyncMode = iota
	// SyncInterval fsyncs in the background every SyncEvery. A crash can
	// lose the last interval's worth of acknowledged writes.
	SyncInterval
)

// Options configure a DB. The zero value is usable.
type Options struct {
	// Retention is how far back AsOf reads are guaranteed to work. Older
	// versions are dropped by compaction. Zero or negative keeps all history
	// forever.
	Retention time.Duration

	// MemtableSize is how much data is buffered in memory before it is
	// flushed to an SSTable. Default 4 MiB.
	MemtableSize int64

	SyncMode  SyncMode
	SyncEvery time.Duration // for SyncInterval; default 100ms

	// MaxImmutableMemtables is how many full memtables may wait for a flush
	// before writers are stalled. Default 2.
	MaxImmutableMemtables int

	BlockSize       int // SSTable data block size, default 4096
	BloomBitsPerKey int // default 10

	Logger *slog.Logger     // default: discard
	Clock  func() time.Time // default time.Now; tests use a fake one

	// compaction tuning; only tests change it, to get deep trees out of
	// small amounts of data.
	compaction compaction.Options
}

func (o Options) withDefaults() Options {
	if o.MemtableSize <= 0 {
		o.MemtableSize = 4 << 20
	}
	if o.SyncEvery <= 0 {
		o.SyncEvery = 100 * time.Millisecond
	}
	if o.MaxImmutableMemtables <= 0 {
		o.MaxImmutableMemtables = 2
	}
	if o.BlockSize <= 0 {
		o.BlockSize = 4096
	}
	if o.BloomBitsPerKey <= 0 {
		o.BloomBitsPerKey = 10
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	return o
}
