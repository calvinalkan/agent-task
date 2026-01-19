package testutil

import "encoding/binary"

// FindKeyForBucketStartIndex returns a deterministic key (length keySize) whose
// FNV-1a hash maps to the desired bucket start index (hash & (bucketCount-1)).
//
// This is used by robustness fuzz tests to force Cache.Get() to touch a specific
// bucket index.
//
// The search is bounded by maxAttempts to keep fuzz iterations fast.
func FindKeyForBucketStartIndex(keySize int, bucketCount, wantIndex uint64, maxAttempts uint32) ([]byte, bool) {
	if keySize <= 0 {
		return nil, false
	}

	// Must be power-of-two for mask math.
	if bucketCount < 2 || (bucketCount&(bucketCount-1)) != 0 {
		return nil, false
	}

	mask := bucketCount - 1
	if wantIndex > mask {
		return nil, false
	}

	key := make([]byte, keySize)

	var tmp [4]byte

	for attempt := range maxAttempts {
		// Deterministic pattern: encode attempt counter into a fixed prefix,
		// then zero the remainder.
		for i := range key {
			key[i] = 0
		}

		binary.BigEndian.PutUint32(tmp[:], attempt)

		if keySize >= 4 {
			copy(key[:4], tmp[:])
		} else {
			copy(key, tmp[4-keySize:])
		}

		h := fnv1a64(key)
		if (h & mask) == wantIndex {
			return append([]byte(nil), key...), true
		}
	}

	return nil, false
}
