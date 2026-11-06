// Package manifest keeps track of which SSTables belong to the database and
// at which level, plus a few counters that must survive restarts.
//
// It is a log of Edits ("add table 12 to L0", "remove tables 7 and 9 from
// L1", "last timestamp is now X"), stored in a MANIFEST-NNNNNN file using
// the same record framing as the WAL. A small CURRENT file names the active
// manifest. Every Load writes a fresh manifest containing one snapshot edit
// and atomically switches CURRENT to it, so the log never grows without
// bound across restarts.
package manifest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/mnazarkhudoev/chronokv/internal/wal"
)

// ErrCorrupted means the manifest can't be trusted. Open must stop: going
// on with a partial file set would make cleanup delete live tables.
var ErrCorrupted = errors.New("manifest: corrupted")

// State holds the counters tracked alongside the file set.
type State struct {
	LogNum, NextFileNum, LastSeq, LastTS, Cutoff uint64
}

// Manifest is safe for concurrent use.
type Manifest struct {
	dir string

	mu      sync.Mutex
	w       *wal.Writer
	name    string
	state   State
	current *Version
	live    map[*Version]struct{}
	hook    func()
}

const currentFile = "CURRENT"

func manifestName(num uint64) string { return fmt.Sprintf("MANIFEST-%06d", num) }

// Create starts a manifest for a brand-new database in dir.
func Create(dir string) (*Manifest, error) {
	m := &Manifest{dir: dir, live: map[*Version]struct{}{}}
	// Normally 1, but a crash during an earlier Create can leave a
	// MANIFEST-000001 behind without a CURRENT pointing at it.
	num := maxFileNum(dir) + 1
	m.state.NextFileNum = num + 1
	v := &Version{}
	if err := m.writeNew(num, v); err != nil {
		return nil, err
	}
	m.install(v)
	return m, nil
}

// Load opens the manifest named by dir/CURRENT, replays it, and rewrites it
// as a single snapshot.
func Load(dir string) (*Manifest, error) {
	cur, err := os.ReadFile(filepath.Join(dir, currentFile))
	if err != nil {
		return nil, fmt.Errorf("manifest: reading CURRENT: %w", err)
	}
	oldName := strings.TrimSpace(string(cur))
	f, err := os.Open(filepath.Join(dir, oldName))
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}

	m := &Manifest{dir: dir, live: map[*Version]struct{}{}}
	v := &Version{}
	r := wal.NewReader(f)
	for {
		_, payload, err := r.Next()
		if err == io.EOF {
			break
		}
		var ce *wal.CorruptError
		if errors.As(err, &ce) {
			// A torn final edit was never acknowledged to anyone (Apply
			// returns only after the fsync), so dropping it is correct.
			// The first record is the snapshot, which was synced before
			// CURRENT ever pointed here, so it can't be torn.
			if ce.Offset > 0 && ce.Tail(st.Size()) {
				break
			}
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupted, oldName, err)
		}
		if err != nil {
			return nil, err
		}
		e, err := decodeEdit(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupted, oldName, err)
		}
		m.state.merge(&e)
		v = v.apply(&e)
	}

	// Never hand out a file number that is already on disk, even if a crash
	// lost the edit that would have recorded it.
	if n := maxFileNum(dir) + 1; n > m.state.NextFileNum {
		m.state.NextFileNum = n
	}
	num := m.state.NextFileNum
	m.state.NextFileNum++
	if err := m.writeNew(num, v); err != nil {
		return nil, err
	}
	m.install(v)
	if oldName != m.name {
		os.Remove(filepath.Join(dir, oldName))
	}
	return m, nil
}

// maxFileNum returns the largest number used by any engine file in dir.
func maxFileNum(dir string) uint64 {
	entries, _ := os.ReadDir(dir)
	var max uint64
	for _, e := range entries {
		name := strings.TrimPrefix(e.Name(), "MANIFEST-")
		end := 0
		for end < len(name) && name[end] >= '0' && name[end] <= '9' {
			end++
		}
		if end == 0 {
			continue
		}
		if n, err := strconv.ParseUint(name[:end], 10, 64); err == nil && n > max {
			max = n
		}
	}
	return max
}

// writeNew writes a fresh manifest file holding a snapshot of v and points
// CURRENT at it.
func (m *Manifest) writeNew(num uint64, v *Version) error {
	name := manifestName(num)
	w, err := wal.Create(filepath.Join(m.dir, name))
	if err != nil {
		return err
	}
	e := v.snapshotEdit(m.state)
	if err := w.Append(0, e.encode()); err != nil {
		w.Close()
		return err
	}
	if err := w.Sync(); err != nil {
		w.Close()
		return err
	}
	if err := setCurrent(m.dir, name); err != nil {
		w.Close()
		return err
	}
	m.w, m.name = w, name
	return nil
}

func setCurrent(dir, name string) error {
	tmp := filepath.Join(dir, currentFile+".tmp")
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(name + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, currentFile)); err != nil {
		return err
	}
	return wal.SyncDir(dir)
}

func (s *State) merge(e *Edit) {
	up := func(dst *uint64, v uint64) {
		if v > *dst {
			*dst = v
		}
	}
	up(&s.LogNum, e.LogNum)
	up(&s.NextFileNum, e.NextFileNum)
	up(&s.LastSeq, e.LastSeq)
	up(&s.LastTS, e.LastTS)
	up(&s.Cutoff, e.Cutoff)
}

// install makes v current. Caller holds m.mu or is the constructor. The
// manifest's own reference to the old current is returned so the caller can
// drop it after unlocking.
func (m *Manifest) install(v *Version) *Version {
	v.m = m
	v.Ref()
	m.live[v] = struct{}{}
	old := m.current
	m.current = v
	return old
}

// Current returns the current version with a reference held for the caller.
func (m *Manifest) Current() *Version {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current.Ref()
	return m.current
}

// State returns the current counters.
func (m *Manifest) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// NewFileNum hands out a fresh file number. It is persisted with the next
// Apply; if the process dies before that, Load's directory scan still
// prevents reuse.
func (m *Manifest) NewFileNum() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.state.NextFileNum
	m.state.NextFileNum++
	return n
}

// Apply durably logs e and installs the resulting version.
func (m *Manifest) Apply(e Edit) error {
	m.mu.Lock()
	if m.w == nil {
		m.mu.Unlock()
		return errors.New("manifest: closed")
	}
	e.NextFileNum = m.state.NextFileNum
	if err := m.w.Append(0, e.encode()); err != nil {
		m.mu.Unlock()
		return err
	}
	if err := m.w.Sync(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.state.merge(&e)
	old := m.install(m.current.apply(&e))
	m.mu.Unlock()
	if old != nil {
		old.Unref()
	}
	return nil
}

// release is called by Version.Unref when a version has no references left.
func (m *Manifest) release(v *Version) {
	m.mu.Lock()
	delete(m.live, v)
	hook := m.hook
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
}

// SetReleaseHook registers fn to be called (without locks held) whenever an
// old version is released, which is when tables may have become deletable.
func (m *Manifest) SetReleaseHook(fn func()) {
	m.mu.Lock()
	m.hook = fn
	m.mu.Unlock()
}

// LiveFiles returns the numbers of all tables referenced by any live version.
func (m *Manifest) LiveFiles() map[uint64]struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := map[uint64]struct{}{}
	for v := range m.live {
		for _, level := range v.Levels {
			for _, f := range level {
				live[f.Num] = struct{}{}
			}
		}
	}
	return live
}

// Close closes the manifest file.
func (m *Manifest) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.w == nil {
		return nil
	}
	err := m.w.Close()
	m.w = nil
	return err
}
