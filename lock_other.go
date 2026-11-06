//go:build !unix

package chronokv

import "io"

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// lockDir is a no-op outside unix. Don't open the same directory twice.
func lockDir(dir string) (io.Closer, error) { return nopCloser{}, nil }
