package chronokv

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mnazarkhudoev/chronokv/internal/wal"
)

func tableName(num uint64) string { return fmt.Sprintf("%06d.sst", num) }

func parseTableName(name string) (uint64, bool) {
	base, ok := strings.CutSuffix(name, ".sst")
	if !ok || base == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(base, 10, 64)
	return n, err == nil
}

// addPending marks a file number as being written right now, so cleanup
// leaves it alone even though no Version references it yet.
func (db *DB) addPending(num uint64) {
	db.mu.Lock()
	db.pending[num] = struct{}{}
	db.mu.Unlock()
}

func (db *DB) removePending(num uint64) {
	db.mu.Lock()
	delete(db.pending, num)
	db.mu.Unlock()
}

// deleteObsoleteFiles removes tables no live Version references and WAL
// segments that are fully flushed. With removeTmp it also removes leftover
// temporary files, which is only safe at Open (later, a .tmp may be a flush
// in progress).
func (db *DB) deleteObsoleteFiles(removeTmp bool) {
	db.obsoleteMu.Lock()
	defer db.obsoleteMu.Unlock()

	entries, err := os.ReadDir(db.dir)
	if err != nil {
		db.log.Warn("listing directory for cleanup", "err", err)
		return
	}
	live := db.man.LiveFiles()
	logNum := db.man.State().LogNum
	db.mu.Lock()
	pending := make(map[uint64]struct{}, len(db.pending))
	for n := range db.pending {
		pending[n] = struct{}{}
	}
	db.mu.Unlock()

	for _, e := range entries {
		name := e.Name()
		var remove bool
		if n, ok := parseTableName(name); ok {
			_, isLive := live[n]
			_, isPending := pending[n]
			remove = !isLive && !isPending
			if remove {
				db.tables.evict(n)
			}
		} else if n, ok := wal.ParseSegmentName(name); ok {
			remove = n < logNum
		} else if removeTmp && strings.HasSuffix(name, ".tmp") {
			remove = true
		}
		if remove {
			if err := os.Remove(filepath.Join(db.dir, name)); err != nil && !os.IsNotExist(err) {
				db.log.Warn("removing obsolete file", "file", name, "err", err)
			}
		}
	}
}
