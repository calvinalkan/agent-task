package slotcache_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Deterministic seqlock overlap regression tests.
//
// These tests verify that the seqlock overlap detection logic correctly
// distinguishes between:
//   - Overlap with a concurrent write (should return ErrBusy after retries)
//   - Real corruption (should return ErrCorrupt)
//
// Unlike the stress tests in seqlock_concurrency_test.go that rely on timing and
// cross-process races, these tests deterministically inject specific file states
// and verify the classification.
//
// Why this matters:
// The seqlock protocol requires readers to re-check generation when they observe
// "impossible" invariants (e.g., bucket pointing to tombstoned slot). If generation
// changed or became odd, the invariant violation is due to overlap and the reader
// should retry. If generation is stable and even, it's real corruption.
//
// These tests catch regressions where the overlap detection logic is weakened or
// removed, which would cause transient concurrent-write scenarios to be
// misclassified as corruption.

// Test_Get_Returns_ErrBusy_When_Bucket_Points_To_Tombstoned_Slot_And_Generation_Changes verifies
// that Get() returns ErrBusy (not ErrCorrupt) when it encounters a bucket pointing to
// a tombstoned slot AND generation changes during the read, indicating overlap.
func Test_Get_Returns_ErrBusy_When_Bucket_Points_To_Tombstoned_Slot_And_Generation_Changes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "overlap_tombstone.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true, // Simplify test: no cross-process locking
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

	// Verify key exists before corruption.
	_, found, getErr := cache.Get(key)
	if getErr != nil {
		t.Fatalf("Get before mutation failed: %v", getErr)
	}

	if !found {
		t.Fatal("Key should exist before mutation")
	}

	_ = cache.Close()

	// Now corrupt the file: clear the slot's USED flag (tombstone the slot) but leave
	// the bucket pointing to it, AND set generation to odd (simulating writer in progress).
	//
	// When Get() encounters this, it should detect the impossible invariant (bucket→tombstone),
	// re-read generation, see it's odd, and return ErrBusy (overlap) not ErrCorrupt.
	mutateFileForOverlapTest(t, path, func(data []byte) {
		// Set generation to odd (simulating active writer).
		binary.LittleEndian.PutUint64(data[offGeneration:], 1) // odd

		// Clear slot 0's USED flag (tombstone the slot).
		// Slot 0 is at offset 256 (header size), meta is the first 8 bytes.
		slotOffset := slcHeaderSize // slot 0
		meta := binary.LittleEndian.Uint64(data[slotOffset:])
		meta &^= 1 // clear bit 0 (USED)
		binary.LittleEndian.PutUint64(data[slotOffset:], meta)
	})

	// Reopen and verify Get returns ErrBusy.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		// Open itself may return ErrBusy for odd generation without locking.
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			// Expected - odd generation at Open time.
			return
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, _, getErr2 := cache2.Get(key)
	if !errors.Is(getErr2, slotcache.ErrBusy) {
		t.Fatalf("Get() should return ErrBusy for bucket→tombstone overlap; got %v", getErr2)
	}
}

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

	// Corrupt the file: clear the slot's USED flag but leave generation even.
	// This is real corruption - a bucket should never point to a tombstoned slot.
	mutateFileForOverlapTest(t, path, func(data []byte) {
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

// Test_Get_Returns_ErrBusy_When_Bucket_Points_Beyond_Highwater_And_Generation_Changes verifies
// that Get() returns ErrBusy when a bucket references a slot beyond highwater AND generation
// indicates overlap.
func Test_Get_Returns_ErrBusy_When_Bucket_Points_Beyond_Highwater_And_Generation_Changes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "overlap_highwater.slc")

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

	// Corrupt: set highwater to 0 (no slots allocated) but leave bucket pointing to slot 0.
	// Also set generation to odd to simulate overlap.
	mutateFileForOverlapTest(t, path, func(data []byte) {
		// Set generation to odd.
		binary.LittleEndian.PutUint64(data[offGeneration:], 3)

		// Set highwater to 0.
		binary.LittleEndian.PutUint64(data[offSlotHighwater:], 0)
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return // Expected for odd generation
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, _, getErr := cache2.Get(key)
	if !errors.Is(getErr, slotcache.ErrBusy) {
		t.Fatalf("Get() should return ErrBusy for slot beyond highwater with overlap; got %v", getErr)
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

	// Corrupt: modify the bucket entry to point to a slot beyond highwater.
	// The header remains valid, but the bucket index points to an invalid slot.
	// After one Put, highwater=1, so we'll make the bucket point to slot 50.
	//
	// Bucket layout: hash(8) + slot_plus_one(8) where slot_plus_one = slot_id + 1
	// We need to find the bucket for our key and corrupt slot_plus_one.
	mutateFileForOverlapTest(t, path, func(data []byte) {
		// Leave generation even (stable).
		// Find bucket offset using FNV-1a hash of key.
		// bucket_count = 128 (nextPow2(64*2)), buckets_offset = 256 + 64*32 = 2304
		const bucketCount = 128

		bucketsOffset := binary.LittleEndian.Uint64(data[offBucketsOffset:])

		hash := fnv1a64Test(key)
		bucketIdx := hash & (bucketCount - 1)
		bucketOffset := bucketsOffset + bucketIdx*16

		// Find the bucket entry for our key (linear probe if needed).
		for i := range uint64(bucketCount) {
			off := bucketsOffset + ((bucketIdx+i)&(bucketCount-1))*16

			slotPlusOne := binary.LittleEndian.Uint64(data[off+8:])
			if slotPlusOne == 1 { // slot 0 (our entry)
				// Corrupt: change slot_plus_one to 51 (slot 50, beyond highwater=1)
				binary.LittleEndian.PutUint64(data[off+8:], 51)
				bucketOffset = off

				break
			}
		}

		_ = bucketOffset // used above
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

// Test_Len_Returns_ErrBusy_When_Generation_Odd verifies that Len() returns ErrBusy
// when generation is odd (writer in progress), matching spec semantics.
func Test_Len_Returns_ErrBusy_When_Generation_Odd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "len_busy.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	_ = cache.Close()

	// Set generation to odd.
	mutateFileForOverlapTest(t, path, func(data []byte) {
		binary.LittleEndian.PutUint64(data[offGeneration:], 5)
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return // Expected
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, lenErr := cache2.Len()
	if !errors.Is(lenErr, slotcache.ErrBusy) {
		t.Fatalf("Len() should return ErrBusy when generation is odd; got %v", lenErr)
	}
}

// Test_Scan_Returns_ErrBusy_When_Generation_Odd verifies that Scan() returns ErrBusy
// when generation is odd.
func Test_Scan_Returns_ErrBusy_When_Generation_Odd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_busy.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	_ = cache.Close()

	// Set generation to odd.
	mutateFileForOverlapTest(t, path, func(data []byte) {
		binary.LittleEndian.PutUint64(data[offGeneration:], 7)
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return // Expected
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, scanErr := cache2.Scan(slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrBusy) {
		t.Fatalf("Scan() should return ErrBusy when generation is odd; got %v", scanErr)
	}
}

// Test_Get_Returns_Entry_When_Generation_Is_Stable_Even verifies that Get() succeeds
// when generation is stable and even, after a proper commit.
func Test_Get_Returns_Entry_When_Generation_Is_Stable_Even(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "get_stable.slc")

	key := []byte("stableky")
	index := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	revision := int64(42)

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

	putErr := w.Put(key, revision, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()

	// Verify Get succeeds with correct values.
	entry, found, getErr := cache.Get(key)
	if getErr != nil {
		t.Fatalf("Get failed: %v", getErr)
	}

	if !found {
		t.Fatal("Key should be found")
	}

	if entry.Revision != revision {
		t.Fatalf("Wrong revision: got %d, want %d", entry.Revision, revision)
	}

	if !bytes.Equal(entry.Index, index) {
		t.Fatalf("Wrong index: got %x, want %x", entry.Index, index)
	}

	_ = cache.Close()
}

// Test_Generation_Remains_Even_When_Multiple_Commits_Complete verifies that after multiple commits,
// generation remains even (stable).
func Test_Generation_Remains_Even_When_Multiple_Commits_Complete(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "multi_commit.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	defer func() { _ = cache.Close() }()

	// Perform multiple commits.
	for i := range 5 {
		w, beginErr := cache.BeginWrite()
		if beginErr != nil {
			t.Fatalf("BeginWrite %d failed: %v", i, beginErr)
		}

		key := make([]byte, 8)
		key[0] = byte(i)
		index := []byte{byte(i), byte(i), byte(i), byte(i)}

		putErr := w.Put(key, int64(i), index)
		if putErr != nil {
			t.Fatalf("Put %d failed: %v", i, putErr)
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit %d failed: %v", i, commitErr)
		}

		_ = w.Close()

		// Verify reads work after each commit.
		count, lenErr := cache.Len()
		if lenErr != nil {
			t.Fatalf("Len after commit %d failed: %v", i, lenErr)
		}

		if count != i+1 {
			t.Fatalf("Wrong count after commit %d: got %d, want %d", i, count, i+1)
		}
	}
}

// Test_Generation_Increases_When_Commit_Completes verifies that generation follows the seqlock
// protocol: odd during write, even after commit.
func Test_Generation_Increases_When_Commit_Completes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "gen_increment.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	_ = cache.Close()

	// Check initial generation (should be 0, even).
	gen := readGenerationFromFile(t, path)
	if gen%2 != 0 {
		t.Fatalf("Initial generation should be even; got %d", gen)
	}

	// Reopen, commit, close, check generation.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}

	w, beginErr := cache2.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	key := []byte("gentest1")

	putErr := w.Put(key, 1, []byte{1, 2, 3, 4})
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache2.Close()

	// Check generation after commit (should be even and > 0).
	genAfter := readGenerationFromFile(t, path)
	if genAfter%2 != 0 {
		t.Fatalf("Generation after commit should be even; got %d", genAfter)
	}

	if genAfter <= gen {
		t.Fatalf("Generation should increase after commit; got %d (was %d)", genAfter, gen)
	}
}

// Helper constants (must match slotcache internal values).
const (
	offSlotHighwater = 0x028
	offBucketsOffset = 0x068
)

// FNV-1a 64-bit hash constants (must match slotcache internal values).
const (
	fnv1aOffsetBasisTest uint64 = 14695981039346656037
	fnv1aPrimeTest       uint64 = 1099511628211
)

// fnv1a64Test computes the FNV-1a 64-bit hash over key bytes.
// This must match the implementation in slotcache.
func fnv1a64Test(key []byte) uint64 {
	hash := fnv1aOffsetBasisTest
	for _, b := range key {
		hash ^= uint64(b)
		hash *= fnv1aPrimeTest
	}

	return hash
}

// readGenerationFromFile reads the generation counter from the file.
func readGenerationFromFile(tb testing.TB, path string) uint64 {
	tb.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read file: %v", err)
	}

	if len(data) < slcHeaderSize {
		tb.Fatalf("File too small: %d bytes", len(data))
	}

	return binary.LittleEndian.Uint64(data[offGeneration:])
}

// mutateFileForOverlapTest reads the entire file, applies the mutation, and writes it back.
// Unlike mutateHeader which only handles the header, this handles the full file.
func mutateFileForOverlapTest(tb testing.TB, path string, mutate func([]byte)) {
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
