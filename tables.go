package chronokv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mnazarkhudoev/chronokv/internal/sstable"
)

// tableCache keeps SSTable readers open. A reader stays open until its file
// is deleted, which only happens once no Version (and therefore no running
// read or iterator) references it.
type tableCache struct {
	dir string
	mu  sync.Mutex
	m   map[uint64]*sstable.Reader
}

func newTableCache(dir string) *tableCache {
	return &tableCache{dir: dir, m: map[uint64]*sstable.Reader{}}
}

func (c *tableCache) get(num uint64) (*sstable.Reader, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r, ok := c.m[num]; ok {
		return r, nil
	}
	path := filepath.Join(c.dir, tableName(num))
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	r, err := sstable.Open(f, st.Size())
	if err != nil {
		f.Close()
		return nil, wrapTableErr(path, err)
	}
	c.m[num] = r
	return r, nil
}

func (c *tableCache) evict(num uint64) {
	c.mu.Lock()
	r, ok := c.m[num]
	delete(c.m, num)
	c.mu.Unlock()
	if ok {
		r.Close()
	}
}

func (c *tableCache) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for n, r := range c.m {
		r.Close()
		delete(c.m, n)
	}
}

// wrapTableErr turns sstable corruption into the public ErrCorrupted.
func wrapTableErr(path string, err error) error {
	if errors.Is(err, sstable.ErrCorrupted) {
		return fmt.Errorf("%w: %s: %v", ErrCorrupted, path, err)
	}
	return err
}
