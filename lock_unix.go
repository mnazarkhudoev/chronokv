//go:build unix

package chronokv

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// lockDir takes an exclusive flock on dir/LOCK so two processes (or two Open
// calls in one process) can't run on the same directory. The lock goes away
// when the file is closed or the process dies.
func lockDir(dir string) (io.Closer, error) {
	f, err := os.OpenFile(filepath.Join(dir, "LOCK"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}
