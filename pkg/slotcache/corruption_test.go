// Corruption detection tests.
//
// These tests verify that slotcache correctly detects and reports file corruption
// via ErrCorrupt. Grouped by type of corruption:
//
// 1. Bucket sampling: Open-time validation that sampled buckets reference valid slots
// 2. Slot meta: Reserved bits in slot meta field must be zero
// 3. Writer safety: Commit detects corrupt bucket table (no empty buckets)
//
// Oracle: ErrCorrupt when file state violates format invariants
// Technique: Direct file mutation + operation that reads corrupted region

package slotcache_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// =============================================================================
// Bucket Sampling Corruption (Open-time validation)
// =============================================================================

func Test_Open_Returns_ErrCorrupt_When_Sampled_Bucket_References_OutOfRange_SlotID(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bucket_corrupt.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64, // bucket_count = nextPow2(64*2) = 128
	}

	// Create a valid cache with some entries.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert a few entries to have some buckets populated.
	for i := range 10 {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, uint64(i))

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i*100))

		putErr := w.Put(key, int64(i), index)
		if putErr != nil {
			t.Fatalf("Put(%d) failed: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// Corrupt a bucket at a sampled position (position 0 is always sampled).
	corruptSampledBucket(t, path)

	// Reopen should fail with ErrCorrupt due to bucket sampling.
	_, err = slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrCorrupt) {
		t.Fatalf("Open(corrupted) error mismatch: got=%v want=%v", err, slotcache.ErrCorrupt)
	}
}

func Test_Open_Succeeds_When_All_Sampled_Buckets_Are_Valid(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "bucket_valid.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create a valid cache with some entries.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	for i := range 10 {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, uint64(i))

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i*100))

		putErr := w.Put(key, int64(i), index)
		if putErr != nil {
			t.Fatalf("Put(%d) failed: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// Reopen should succeed - file is not corrupted.
	c2, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(reopen) failed: %v", err)
	}

	defer c2.Close()

	// Verify data is intact.
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, 5)

	entry, found, err := c2.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !found {
		t.Fatal("Get: key not found")
	}

	if entry.Revision != 5 {
		t.Fatalf("Get: revision mismatch: got=%d want=%d", entry.Revision, 5)
	}
}

func Test_Open_Returns_ErrCorrupt_When_Bucket_At_Multiple_Sample_Positions_Are_Invalid(t *testing.T) {
	t.Parallel()

	// Test corruption at different sampled positions.
	// With bucketSampleCount=8 and bucketCount=128, step=16.
	// Sampled positions: 0, 16, 32, 48, 64, 80, 96, 112.

	samplePositions := []uint64{0, 16, 32, 48, 64, 80, 96, 112}

	for _, pos := range samplePositions {
		t.Run("position_"+string(rune('0'+pos/16)), func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "bucket_pos.slc")

			opts := slotcache.Options{
				Path:         path,
				KeySize:      8,
				IndexSize:    4,
				UserVersion:  1,
				SlotCapacity: 64,
			}

			// Create a valid cache.
			c, err := slotcache.Open(opts)
			if err != nil {
				t.Fatalf("Open(create) failed: %v", err)
			}

			closeErr := c.Close()
			if closeErr != nil {
				t.Fatalf("Close failed: %v", closeErr)
			}

			// Corrupt bucket at the specified sampled position.
			corruptBucketAtPosition(t, path, pos)

			// Reopen should fail.
			_, err = slotcache.Open(opts)
			if !errors.Is(err, slotcache.ErrCorrupt) {
				t.Fatalf("Open(corrupted at pos %d) error mismatch: got=%v want=%v", pos, err, slotcache.ErrCorrupt)
			}
		})
	}
}

// corruptSampledBucket corrupts bucket 0 (always sampled) to reference an out-of-range slot ID.
func corruptSampledBucket(t *testing.T, path string) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	defer f.Close()

	// Read header to get bucketsOffset and slotHighwater.
	headerBuf := make([]byte, 256)

	_, readErr := f.ReadAt(headerBuf, 0)
	if readErr != nil {
		t.Fatalf("ReadAt(header) failed: %v", readErr)
	}

	bucketsOffset := binary.LittleEndian.Uint64(headerBuf[offBucketsOffset:])
	slotHighwater := binary.LittleEndian.Uint64(headerBuf[offSlotHighwater:])

	// Corrupt bucket at position 0 (always sampled).
	// Write a FULL bucket entry with an invalid slot reference.
	bucketBuf := make([]byte, 16)
	binary.LittleEndian.PutUint64(bucketBuf[0:], 0x12345678ABCDEF00) // hash64
	binary.LittleEndian.PutUint64(bucketBuf[8:], slotHighwater+100)  // slot_plus1 (invalid)

	offset := int64(bucketsOffset) // bucket 0

	_, writeErr := f.WriteAt(bucketBuf, offset)
	if writeErr != nil {
		t.Fatalf("WriteAt(corrupt bucket) failed: %v", writeErr)
	}

	t.Logf("Corrupted bucket 0: set slot_plus1=%d (slot_id=%d, highwater=%d)",
		slotHighwater+100, slotHighwater+99, slotHighwater)
}

// corruptBucketAtPosition corrupts a specific bucket to have an invalid slot reference.
func corruptBucketAtPosition(t *testing.T, path string, bucketIdx uint64) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	defer f.Close()

	// Read header.
	headerBuf := make([]byte, 256)

	_, readErr := f.ReadAt(headerBuf, 0)
	if readErr != nil {
		t.Fatalf("ReadAt(header) failed: %v", readErr)
	}

	bucketsOffset := binary.LittleEndian.Uint64(headerBuf[offBucketsOffset:])
	slotHighwater := binary.LittleEndian.Uint64(headerBuf[offSlotHighwater:])

	// Write invalid bucket data at the specified position.
	offset := int64(bucketsOffset + bucketIdx*16)
	bucketBuf := make([]byte, 16)

	// Set hash64 to some value.
	binary.LittleEndian.PutUint64(bucketBuf[0:], 0x12345678ABCDEF00)
	// Set slot_plus1 to an invalid value (way beyond highwater).
	binary.LittleEndian.PutUint64(bucketBuf[8:], slotHighwater+1000)

	_, writeErr := f.WriteAt(bucketBuf, offset)
	if writeErr != nil {
		t.Fatalf("WriteAt(corrupt bucket) failed: %v", writeErr)
	}

	t.Logf("Corrupted bucket at position %d with invalid slot reference", bucketIdx)
}

// =============================================================================
// Slot Meta Corruption (Reserved bits validation)
// =============================================================================

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

	w, beginErr := cache.Writer()
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

	w, beginErr := cache.Writer()
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

	w, beginErr := cache.Writer()
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

	w, beginErr := cache.Writer()
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

	w, beginErr := cache.Writer()
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
	w2, beginErr := cache.Writer()
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
func corruptSlotMeta(tb testing.TB, path string, transform func(uint64) uint64) {
	tb.Helper()

	mutateFile(tb, path, func(data []byte) {
		slotOffset := slcHeaderSize
		meta := binary.LittleEndian.Uint64(data[slotOffset:])
		newMeta := transform(meta)
		binary.LittleEndian.PutUint64(data[slotOffset:], newMeta)
	})
}

// =============================================================================
// Writer Safety Corruption (Commit-time detection)
// =============================================================================

func Test_WriterCommit_Returns_ErrCorrupt_When_No_Empty_Buckets_Are_Available(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "writer_no_empty_buckets.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create a valid file with one live slot so we can point buckets at slot 0.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	putErr := w.Put([]byte("key00000"), 1, nil)
	if putErr != nil {
		_ = w.Close()
		_ = c.Close()

		t.Fatalf("Put failed: %v", putErr)
	}

	createCommitErr := w.Commit()
	if createCommitErr != nil {
		_ = w.Close()
		_ = c.Close()

		t.Fatalf("Commit(create) failed: %v", createCommitErr)
	}

	_ = w.Close()
	_ = c.Close()

	// Corrupt the bucket table by making every bucket FULL and pointing to slot 0.
	// Keep the header counters unchanged so Open() succeeds (lightweight validation).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	bucketCount := binary.LittleEndian.Uint64(data[offBucketCount : offBucketCount+8])
	bucketsOffset := binary.LittleEndian.Uint64(data[offBucketsOffset : offBucketsOffset+8])

	// Sanity: the file should have at least one allocated slot.
	highwater := binary.LittleEndian.Uint64(data[offSlotHighwater : offSlotHighwater+8])
	if highwater != 1 {
		t.Fatalf("expected slot_highwater=1 after initial commit, got %d", highwater)
	}

	for i := range bucketCount {
		bucketOff := bucketsOffset + i*16
		off := int(bucketOff)

		binary.LittleEndian.PutUint64(data[off:off+8], 0)    // hash (ignored here)
		binary.LittleEndian.PutUint64(data[off+8:off+16], 1) // slot_plus1 -> slot 0
	}

	writeErr := os.WriteFile(path, data, 0o600)
	if writeErr != nil {
		t.Fatalf("write file: %v", writeErr)
	}

	// Reopen (should succeed) and attempt a new insert.
	c, err = slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(corrupt buckets) failed: %v", err)
	}

	defer func() { _ = c.Close() }()

	before := mustReadFile(t, path)
	beforeHighwater := binary.LittleEndian.Uint64(before[offSlotHighwater : offSlotHighwater+8])
	beforeLive := binary.LittleEndian.Uint64(before[offLiveCount : offLiveCount+8])

	w, err = c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	_ = w.Put([]byte("key00001"), 2, nil)

	commitErr := w.Commit()
	_ = w.Close()

	if !errors.Is(commitErr, slotcache.ErrCorrupt) {
		t.Fatalf("Commit() error mismatch: got=%v want=%v", commitErr, slotcache.ErrCorrupt)
	}

	// Even though the file is corrupt, the writer must not have partially
	// published header counters (slot_highwater/live_count).
	after := mustReadFile(t, path)
	afterHighwater := binary.LittleEndian.Uint64(after[offSlotHighwater : offSlotHighwater+8])
	afterLive := binary.LittleEndian.Uint64(after[offLiveCount : offLiveCount+8])

	if afterHighwater != beforeHighwater {
		t.Fatalf("slot_highwater changed on failed commit: before=%d after=%d", beforeHighwater, afterHighwater)
	}

	if afterLive != beforeLive {
		t.Fatalf("live_count changed on failed commit: before=%d after=%d", beforeLive, afterLive)
	}
}

// =============================================================================
// Seqlock Overlap Corruption (bucket/slot invariant violations with stable generation)
// =============================================================================

// Test_Get_Returns_ErrCorrupt_When_Bucket_Points_To_Tombstoned_Slot_And_Generation_Stable verifies
// that Get() returns ErrCorrupt (not ErrBusy) when it encounters a bucket pointing to
// a tombstoned slot AND generation is stable (same even value), indicating real corruption.
func Test_Get_Returns_ErrCorrupt_When_Bucket_Points_To_Tombstoned_Slot_And_Generation_Stable(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "corrupt_tombstone.slc")

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

	w, beginErr := cache.Writer()
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

	// Corrupt the file: clear the slot's USED flag but leave generation even.
	// This is real corruption - a bucket should never point to a tombstoned slot.
	mutateFile(t, path, func(data []byte) {
		// Leave generation even (stable state).
		// The committed cache should have generation = 2 (after one commit).

		// Clear slot 0's USED flag (tombstone the slot).
		slotOffset := slcHeaderSize
		meta := binary.LittleEndian.Uint64(data[slotOffset:])
		meta &^= 1 // clear bit 0 (USED)
		binary.LittleEndian.PutUint64(data[slotOffset:], meta)
	})

	// Reopen and verify Get returns ErrCorrupt.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, _, getErr := cache2.Get(key)
	if !errors.Is(getErr, slotcache.ErrCorrupt) {
		t.Fatalf("Get() should return ErrCorrupt for bucket→tombstone with stable generation; got %v", getErr)
	}
}

// Test_Get_Returns_ErrCorrupt_When_Bucket_Points_Beyond_Highwater_And_Generation_Stable verifies
// that Get() returns ErrCorrupt when a bucket references a slot beyond highwater AND generation
// is stable (real corruption).
func Test_Get_Returns_ErrCorrupt_When_Bucket_Points_Beyond_Highwater_And_Generation_Stable(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "corrupt_highwater.slc")

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

	w, beginErr := cache.Writer()
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

	// Corrupt: modify the bucket entry to point to a slot beyond highwater.
	// The header remains valid, but the bucket index points to an invalid slot.
	// After one Put, highwater=1, so we'll make the bucket point to slot 50.
	mutateFile(t, path, func(data []byte) {
		// Leave generation even (stable).
		// Find bucket offset using FNV-1a hash of key.
		const bucketCount = 128

		bucketsOffset := binary.LittleEndian.Uint64(data[offBucketsOffset:])

		hash := fnv1a64(key)
		bucketIdx := hash & (bucketCount - 1)

		// Find the bucket entry for our key (linear probe if needed).
		for i := range uint64(bucketCount) {
			off := bucketsOffset + ((bucketIdx+i)&(bucketCount-1))*16

			slotPlusOne := binary.LittleEndian.Uint64(data[off+8:])
			if slotPlusOne == 1 { // slot 0 (our entry)
				// Corrupt: change slot_plus_one to 51 (slot 50, beyond highwater=1)
				binary.LittleEndian.PutUint64(data[off+8:], 51)

				break
			}
		}
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, _, getErr := cache2.Get(key)
	if !errors.Is(getErr, slotcache.ErrCorrupt) {
		t.Fatalf("Get() should return ErrCorrupt for slot beyond highwater with stable gen; got %v", getErr)
	}
}
