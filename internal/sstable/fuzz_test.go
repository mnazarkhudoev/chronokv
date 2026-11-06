package sstable

import (
	"bytes"
	"testing"

	"github.com/mnazarkhudoev/chronokv/internal/keys"
)

func FuzzOpen(f *testing.F) {
	var small []entry
	for i := 0; i < 20; i++ {
		small = append(small, entry{keys.Encode(nil, []byte{byte('a' + i)}, 1, keys.KindSet), []byte("v")})
	}
	data, _ := build(f, small, WriterOptions{BlockSize: 64})
	f.Add(data)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		r, err := Open(memFile{bytes.NewReader(data)}, int64(len(data)))
		if err != nil {
			return
		}
		it := r.NewIter()
		for ok := it.First(); ok; ok = it.Next() {
		}
		it.SeekGE([]byte("m"))
		r.MayContain([]byte("a\x00\x00"))
	})
}
