package sstable

import "math"

// A plain bloom filter with double hashing: one 64-bit FNV-1a hash is split
// into two 32-bit halves and probe i uses h1 + i*h2. The encoded form is the
// bit array followed by one byte holding the number of probes.

func bloomHash(b []byte) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, c := range b {
		h ^= uint64(c)
		h *= prime
	}
	// FNV's low bits mix poorly for short, similar keys; one round of a
	// murmur-style finalizer fixes the false positive rate noticeably.
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	return h
}

func buildFilter(hashes []uint64, bitsPerKey int) []byte {
	k := int(math.Round(float64(bitsPerKey) * 0.69)) // ~ln(2) * bits/key
	k = min(max(k, 1), 30)
	nbits := max(len(hashes)*bitsPerKey, 64)
	nbytes := (nbits + 7) / 8
	nbits = nbytes * 8
	filter := make([]byte, nbytes+1)
	for _, h := range hashes {
		h1, h2 := uint32(h), uint32(h>>32)
		for i := 0; i < k; i++ {
			bit := (h1 + uint32(i)*h2) % uint32(nbits)
			filter[bit/8] |= 1 << (bit % 8)
		}
	}
	filter[nbytes] = byte(k)
	return filter
}

func filterMayContain(filter []byte, h uint64) bool {
	if len(filter) < 2 {
		return true // no usable filter: can't rule anything out
	}
	nbits := uint32(len(filter)-1) * 8
	k := int(filter[len(filter)-1])
	if k < 1 || k > 30 {
		return true
	}
	h1, h2 := uint32(h), uint32(h>>32)
	for i := 0; i < k; i++ {
		bit := (h1 + uint32(i)*h2) % nbits
		if filter[bit/8]&(1<<(bit%8)) == 0 {
			return false
		}
	}
	return true
}
