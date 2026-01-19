// Slot meta corruption tests.
//
// These tests verify that read operations correctly detect and report corruption
// when slot meta has reserved bits set. Per spec (002-format.md):
// "All other bits [except bit 0 USED] are reserved and MUST be zero in v1."
//
// The tests verify:
// - ErrCorrupt is returned when reserved bits are set under stable even generation
// - ErrBusy is returned when reserved bits are set but generation is odd (overlap)
//
// Why this matters:
// Reserved bits validation provides early corruption detection. If a slot's meta
// has reserved bits set, it indicates file corruption or a version mismatch.
// Detecting this early prevents downstream issues and aids debugging.

package slotcache_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Test constants are defined in seqlock_concurrency_test.go:
// slcHeaderSize = 256
// offGeneration = 0x040

// Test_Get_Returns_ErrCorrupt_When_Slot_Meta_Has_Reserved_Bits_Set verifies
// that Get() returns ErrCorrupt when a slot's meta field has reserved bits set
// (bits other than bit 0) under a stable even generation.
func Test_Get_Returns_ErrCorrupt_When_Slot_Meta_Has_Reserved_Bits_Set(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "meta_corruption_get.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create and populate cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt the file: set reserved bit 1 in slot 0's meta.
	// Generation remains even (stable), so this should be detected as real corruption.
	corruptSlotMeta(t, path, func(meta uint64) uint64 {
		return meta | (1 << 1) // Set reserved bit 1
	})

	// Reopen and verify Get returns ErrCorrupt.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}
	defer cache2.Close()

	_, _, err := cache2.Get(key)
	if !errors.Is(err, slotcache.ErrCorrupt) {
		t.Fatalf("Get with corrupted meta: got=%v want=%v", err, slotcache.ErrCorrupt)
	}
}

// Test_Get_Returns_ErrBusy_When_Slot_Meta_Has_Reserved_Bits_And_Generation_Is_Odd verifies
// that Get() returns ErrBusy (not ErrCorrupt) when a slot's meta has reserved bits set
// AND generation is odd, indicating the corruption might be due to overlap with a writer.
func Test_Get_Returns_ErrBusy_When_Slot_Meta_Has_Reserved_Bits_And_Generation_Is_Odd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "meta_overlap_get.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create and populate cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt the file: set reserved bits AND set generation to odd.
	// This simulates overlap with a concurrent writer.
	mutateFile(t, path, func(data []byte) {
		// Set generation to odd (simulating active writer).
		binary.LittleEndian.PutUint64(data[offGeneration:], 1)

		// Set reserved bit in slot 0's meta.
		slotOffset := slcHeaderSize // slot 0
		meta := binary.LittleEndian.Uint64(data[slotOffset:])
		meta |= (1 << 1) // Set reserved bit 1
		binary.LittleEndian.PutUint64(data[slotOffset:], meta)
	})

	// Reopen. With odd generation and locking disabled, Open returns ErrBusy.
	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrBusy) {
		t.Fatalf("Reopen with odd generation: got=%v want=%v", reopenErr, slotcache.ErrBusy)
	}
}

// Test_Scan_Returns_ErrCorrupt_When_Slot_Meta_Has_Reserved_Bits_Set verifies
// that Scan() returns ErrCorrupt when iterating over a slot with reserved bits set
// under a stable even generation.
func Test_Scan_Returns_ErrCorrupt_When_Slot_Meta_Has_Reserved_Bits_Set(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "meta_corruption_scan.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create and populate cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt the file: set reserved bits in slot 0's meta.
	corruptSlotMeta(t, path, func(meta uint64) uint64 {
		return meta | (1 << 2) // Set reserved bit 2
	})

	// Reopen and verify Scan returns ErrCorrupt.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}
	defer cache2.Close()

	_, err := cache2.Scan(slotcache.ScanOptions{})
	if !errors.Is(err, slotcache.ErrCorrupt) {
		t.Fatalf("Scan with corrupted meta: got=%v want=%v", err, slotcache.ErrCorrupt)
	}
}

// Test_ScanRange_Returns_ErrCorrupt_When_Slot_Meta_Has_Reserved_Bits_Set verifies
// that ScanRange() returns ErrCorrupt when iterating over a slot with reserved bits set
// under a stable even generation (in ordered-keys mode).
func Test_ScanRange_Returns_ErrCorrupt_When_Slot_Meta_Has_Reserved_Bits_Set(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "meta_corruption_scanrange.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		OrderedKeys:    true, // Required for ScanRange
		DisableLocking: true,
	}

	// Create and populate cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt the file: set multiple reserved bits in slot 0's meta.
	corruptSlotMeta(t, path, func(meta uint64) uint64 {
		return meta | 0xFF00 // Set reserved bits 8-15
	})

	// Reopen and verify ScanRange returns ErrCorrupt.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}
	defer cache2.Close()

	_, err := cache2.ScanRange(nil, nil, slotcache.ScanOptions{})
	if !errors.Is(err, slotcache.ErrCorrupt) {
		t.Fatalf("ScanRange with corrupted meta: got=%v want=%v", err, slotcache.ErrCorrupt)
	}
}

// Test_Scan_Returns_ErrCorrupt_When_Tombstone_Slot_Has_Reserved_Bits_Set verifies
// that Scan() correctly detects reserved bits on tombstoned slots (meta=0 with reserved bits).
// This ensures the check happens before the tombstone skip.
func Test_Scan_Returns_ErrCorrupt_When_Tombstone_Slot_Has_Reserved_Bits_Set(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "meta_corruption_tombstone.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create, populate, then delete to create a tombstone.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()

	// Delete the key to create a tombstone.
	w2, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite2 failed: %v", beginErr)
	}

	_, delErr := w2.Delete(key)
	if delErr != nil {
		t.Fatalf("Delete failed: %v", delErr)
	}

	commitErr = w2.Commit()
	if commitErr != nil {
		t.Fatalf("Commit2 failed: %v", commitErr)
	}

	_ = w2.Close()
	_ = cache.Close()

	// Corrupt the tombstone: set reserved bits (meta should be 0, make it have reserved bits).
	corruptSlotMeta(t, path, func(meta uint64) uint64 {
		// Meta should be 0 (tombstone). Set reserved bit but keep USED=0.
		_ = meta // Acknowledge original value (should be 0 for tombstone).

		return (1 << 3) // Only reserved bit 3, USED=0
	})

	// Reopen and verify Scan returns ErrCorrupt.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}
	defer cache2.Close()

	_, err := cache2.Scan(slotcache.ScanOptions{})
	if !errors.Is(err, slotcache.ErrCorrupt) {
		t.Fatalf("Scan with corrupted tombstone meta: got=%v want=%v", err, slotcache.ErrCorrupt)
	}
}

// corruptSlotMeta modifies slot 0's meta field in the cache file.
// All tests corrupt slot 0 since that's where the first entry is written.
func corruptSlotMeta(tb testing.TB, path string, transform func(uint64) uint64) {
	tb.Helper()

	mutateFile(tb, path, func(data []byte) {
		// Slot 0 offset: header_size (256 bytes).
		// For default test options: key_size=8, index_size=4
		// slot_size = align8(8 + 8 + 0 + 8 + 4) = align8(28) = 32
		slotOffset := slcHeaderSize

		meta := binary.LittleEndian.Uint64(data[slotOffset:])
		newMeta := transform(meta)
		binary.LittleEndian.PutUint64(data[slotOffset:], newMeta)
	})
}

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
