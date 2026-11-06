package chronokv

import "errors"

var (
	// ErrNotFound means the key did not exist at the requested time.
	ErrNotFound = errors.New("chronokv: not found")

	// ErrHistoryTruncated means the requested timestamp is older than the
	// retention window, so the answer may already have been compacted away.
	// chronokv refuses to guess rather than return something that might be
	// wrong.
	ErrHistoryTruncated = errors.New("chronokv: history before the retention window has been discarded")

	ErrClosed        = errors.New("chronokv: database is closed")
	ErrCorrupted     = errors.New("chronokv: data corruption detected")
	ErrReadOnly      = errors.New("chronokv: database is read-only")
	ErrLocked        = errors.New("chronokv: database directory is locked by another process")
	ErrBatchTooLarge = errors.New("chronokv: batch too large")
)
