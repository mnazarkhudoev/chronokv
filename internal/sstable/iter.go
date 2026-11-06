package sstable

// Iter walks a table: an iterator over the index block picks the data
// block, an iterator over that block walks the entries.
type Iter struct {
	r     *Reader
	index *blockIter
	data  *blockIter
	err   error
}

func (it *Iter) setErr(err error) bool {
	if it.err == nil {
		it.err = err
	}
	it.data = nil
	return false
}

// loadBlock opens the data block the index iterator points at.
func (it *Iter) loadBlock() bool {
	if !it.index.valid {
		if it.index.err != nil {
			return it.setErr(it.index.err)
		}
		it.data = nil
		return false
	}
	h, err := decodeHandle(it.index.value)
	if err != nil {
		return it.setErr(err)
	}
	b, err := it.r.readBlock(h)
	if err != nil {
		return it.setErr(err)
	}
	if it.data, err = newBlockIter(b); err != nil {
		return it.setErr(err)
	}
	return true
}

// skipForward moves to the next block while the current one is exhausted.
func (it *Iter) skipForward() bool {
	for it.data != nil && !it.data.valid {
		if it.data.err != nil {
			return it.setErr(it.data.err)
		}
		if !it.index.Next() {
			if it.index.err != nil {
				return it.setErr(it.index.err)
			}
			it.data = nil
			return false
		}
		if !it.loadBlock() {
			return false
		}
		it.data.First()
	}
	return it.data != nil
}

func (it *Iter) First() bool {
	if it.err != nil {
		return false
	}
	it.index.First()
	if !it.loadBlock() {
		return false
	}
	it.data.First()
	return it.skipForward()
}

func (it *Iter) SeekGE(key []byte) bool {
	if it.err != nil {
		return false
	}
	it.index.SeekGE(key)
	if !it.loadBlock() {
		return false
	}
	it.data.SeekGE(key)
	return it.skipForward()
}

func (it *Iter) Next() bool {
	if it.err != nil || it.data == nil {
		return false
	}
	it.data.Next()
	return it.skipForward()
}

func (it *Iter) Valid() bool   { return it.err == nil && it.data != nil && it.data.valid }
func (it *Iter) Key() []byte   { return it.data.key }
func (it *Iter) Value() []byte { return it.data.value }
func (it *Iter) Error() error  { return it.err }
func (it *Iter) Close() error  { it.data = nil; return nil }
