package wal

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func FuzzReader(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 'x'})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(bytes.NewReader(data))
		for {
			_, _, err := r.Next()
			if err == nil {
				continue
			}
			var ce *CorruptError
			if err != io.EOF && !errors.As(err, &ce) {
				t.Fatalf("unexpected error type %T: %v", err, err)
			}
			return
		}
	})
}
