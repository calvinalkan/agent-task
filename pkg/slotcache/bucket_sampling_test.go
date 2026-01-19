// Bucket sampling: unit tests for Open-time bucket validation
//
// Oracle: ErrCorrupt when sampled bucket references out-of-range slot ID
// Technique: direct file corruption + reopen
//
// These tests verify that Open() performs bucket sampling to detect obvious
// corruptions where bucket entries reference slot IDs beyond slot_highwater.
// This is a cheap O(1) check that fails-fast on common corruptions.
//
// Failures here mean: "Open accepted a corrupted file with invalid bucket references"

package slotcache_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

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

	w, err := c.BeginWrite()
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

	bucketsOffset := binary.LittleEndian.Uint64(headerBuf[0x068:])
	slotHighwater := binary.LittleEndian.Uint64(headerBuf[0x028:])

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

	w, err := c.BeginWrite()
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

	bucketsOffset := binary.LittleEndian.Uint64(headerBuf[0x068:])
	slotHighwater := binary.LittleEndian.Uint64(headerBuf[0x028:])

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
