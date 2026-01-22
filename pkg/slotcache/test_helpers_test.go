// test_helpers_test.go - Shared constants and helper functions for slotcache tests.

package slotcache_test

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"testing"
)

// =============================================================================
// Header layout constants (must match format.go)
// =============================================================================

const (
	slcHeaderSize    = 256
	offKeySize       = 0x00C
	offIndexSize     = 0x010
	offSlotSize      = 0x014
	offSlotCapacity  = 0x020
	offSlotHighwater = 0x028
	offLiveCount     = 0x030
	offGeneration    = 0x040
	offBucketCount   = 0x048
	offSlotsOffset   = 0x060
	offBucketsOffset = 0x068
	offHeaderCRC32C  = 0x070
	offState         = 0x074
	offUserFlags     = 0x078
	offUserData      = 0x080
	offReservedTail  = 0x0C0
)

// FNV-1a 64-bit hash constants (must match slotcache internal values).
const (
	fnv1aOffsetBasis uint64 = 14695981039346656037
	fnv1aPrime       uint64 = 1099511628211
)

// =============================================================================
// File mutation helpers
// =============================================================================

// mutateFile reads a file, applies a mutation, and writes it back.
func mutateFile(tb testing.TB, path string, mutate func([]byte)) {
	tb.Helper()

	data, readErr := os.ReadFile(path)
	if readErr != nil {
		tb.Fatalf("read file: %v", readErr)
	}

	mutate(data)

	writeErr := os.WriteFile(path, data, 0o600)
	if writeErr != nil {
		tb.Fatalf("write file: %v", writeErr)
	}
}

// mutateHeader reads the first 256 bytes (header), applies mutate, then writes it back.
// This does NOT fix the CRC - use mutateHeaderAndFixCRC if the mutation affects CRC-covered fields.
func mutateHeader(tb testing.TB, path string, mutate func([]byte)) {
	tb.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		tb.Fatalf("open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	hdr := make([]byte, slcHeaderSize)

	n, err := f.ReadAt(hdr, 0)
	if err != nil {
		tb.Fatalf("read header: %v", err)
	}

	if n != slcHeaderSize {
		tb.Fatalf("read header size mismatch: got=%d want=%d", n, slcHeaderSize)
	}

	mutate(hdr)

	_, err = f.WriteAt(hdr, 0)
	if err != nil {
		tb.Fatalf("write header: %v", err)
	}

	_ = f.Sync()
}

// mutateHeaderAndFixCRC modifies the header using mutate, then recalculates
// and fixes the header CRC so Open() doesn't reject the file as corrupt.
func mutateHeaderAndFixCRC(tb testing.TB, path string, mutate func([]byte)) {
	tb.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		tb.Fatalf("open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	hdr := make([]byte, slcHeaderSize)

	n, err := f.ReadAt(hdr, 0)
	if err != nil {
		tb.Fatalf("read header: %v", err)
	}

	if n != slcHeaderSize {
		tb.Fatalf("read header size mismatch: got=%d want=%d", n, slcHeaderSize)
	}

	mutate(hdr)

	// Recompute header CRC32-C with generation and crc fields zeroed.
	tmp := make([]byte, slcHeaderSize)
	copy(tmp, hdr)

	// Zero generation field (offset 0x040, 8 bytes).
	for i := offGeneration; i < offGeneration+8; i++ {
		tmp[i] = 0
	}

	// Zero CRC field (offset 0x070, 4 bytes).
	for i := offHeaderCRC32C; i < offHeaderCRC32C+4; i++ {
		tmp[i] = 0
	}

	crc := crc32.Checksum(tmp, crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(hdr[offHeaderCRC32C:offHeaderCRC32C+4], crc)

	_, err = f.WriteAt(hdr, 0)
	if err != nil {
		tb.Fatalf("write header: %v", err)
	}

	_ = f.Sync()
}

// =============================================================================
// File reading helpers
// =============================================================================

// mustReadFile reads a file and fails the test if it can't.
func mustReadFile(tb testing.TB, path string) []byte {
	tb.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read file: %v", err)
	}

	if len(b) < slcHeaderSize {
		tb.Fatalf("file too small: got %d bytes", len(b))
	}

	return b
}

// =============================================================================
// Hash helpers
// =============================================================================

// fnv1a64 computes the FNV-1a 64-bit hash over key bytes.
func fnv1a64(key []byte) uint64 {
	hash := fnv1aOffsetBasis
	for _, b := range key {
		hash ^= uint64(b)
		hash *= fnv1aPrime
	}

	return hash
}
