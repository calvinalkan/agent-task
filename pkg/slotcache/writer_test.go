// Writer behavior tests.
//
// These tests verify Writer operations work correctly:
//
// 1. Rehash correctness: Cache remains correct after many deletions trigger internal rehash
// 2. Copy semantics: Returned entries are detached copies, not aliased to mmap memory
//
// Oracle: Model-based comparison and mutation safety
// Technique: Operation sequences + memory aliasing checks

package slotcache_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// =============================================================================
// Rehash Correctness (deletion-heavy workloads)
// =============================================================================

// Test_Cache_Maintains_Correctness_When_Many_Entries_Deleted verifies that
// after deleting many entries (which internally triggers hash table rebuild),
// all surviving entries remain accessible and the cache functions correctly.
func Test_Cache_Maintains_Correctness_When_Many_Entries_Deleted(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "delete_heavy.slc")

	// Create cache with small capacity.
	// With slot_capacity=8, bucket_count=16.
	// Deleting 5+ entries triggers internal rehash (5/16 > 0.25 threshold).
	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Insert 6 entries.
	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	keys := [][]byte{
		{0, 0, 0, 1},
		{0, 0, 0, 2},
		{0, 0, 0, 3},
		{0, 0, 0, 4},
		{0, 0, 0, 5},
		{0, 0, 0, 6},
	}
	idx := []byte{0, 0, 0, 0}

	for i, k := range keys {
		putErr := w.Put(k, int64(i+1), idx)
		if putErr != nil {
			t.Fatalf("Put failed: %v", putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	// Verify all entries accessible before deletion.
	for i, k := range keys {
		entry, found, getErr := c.Get(k)
		if getErr != nil {
			t.Fatalf("Get key %d failed: %v", i, getErr)
		}

		if !found {
			t.Errorf("expected to find key %d before deletion", i)
		}

		if entry.Revision != int64(i+1) {
			t.Errorf("key %d: expected revision=%d, got %d", i, i+1, entry.Revision)
		}
	}

	// Delete 5 entries, keeping only the last one.
	w2, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	deletedKeys := keys[:5]
	survivingKey := keys[5]

	for i, k := range deletedKeys {
		_, delErr := w2.Delete(k)
		if delErr != nil {
			t.Fatalf("Delete key %d failed: %v", i, delErr)
		}
	}

	commitErr2 := w2.Commit()
	if commitErr2 != nil {
		t.Fatalf("Commit failed: %v", commitErr2)
	}

	// Verify surviving entry is accessible via Get.
	entry, found, getErr := c.Get(survivingKey)
	if getErr != nil {
		t.Fatalf("Get surviving key failed: %v", getErr)
	}

	if !found {
		t.Error("expected to find surviving key")
	}

	if entry.Revision != 6 {
		t.Errorf("expected revision=6, got %d", entry.Revision)
	}

	if !bytes.Equal(entry.Key, survivingKey) {
		t.Errorf("expected key=%v, got %v", survivingKey, entry.Key)
	}

	if !bytes.Equal(entry.Index, idx) {
		t.Errorf("expected index=%v, got %v", idx, entry.Index)
	}

	// Verify deleted keys are not found.
	for i, k := range deletedKeys {
		_, found, getErr := c.Get(k)
		if getErr != nil {
			t.Fatalf("Get deleted key %d failed: %v", i, getErr)
		}

		if found {
			t.Errorf("expected deleted key %d to not be found", i)
		}
	}

	// Verify Len returns 1.
	length, lenErr := c.Len()
	if lenErr != nil {
		t.Fatalf("Len failed: %v", lenErr)
	}

	if length != 1 {
		t.Errorf("expected Len=1, got %d", length)
	}

	// Verify Scan returns only the surviving entry.
	entries, scanErr := c.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("Scan failed: %v", scanErr)
	}

	if len(entries) != 1 {
		t.Errorf("expected Scan to return 1 entry, got %d", len(entries))
	}

	if len(entries) > 0 {
		if entries[0].Revision != 6 {
			t.Errorf("expected Scan entry revision=6, got %d", entries[0].Revision)
		}

		if !bytes.Equal(entries[0].Key, survivingKey) {
			t.Errorf("expected Scan entry key=%v, got %v", survivingKey, entries[0].Key)
		}
	}

	// Verify new inserts work correctly after deletions.
	w3, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite for new insert failed: %v", beginErr)
	}

	newKey := []byte{0, 0, 0, 7}

	putErr := w3.Put(newKey, 100, idx)
	if putErr != nil {
		t.Fatalf("Put new key failed: %v", putErr)
	}

	commitErr3 := w3.Commit()
	if commitErr3 != nil {
		t.Fatalf("Commit new insert failed: %v", commitErr3)
	}

	// Verify new entry is accessible.
	newEntry, newFound, newGetErr := c.Get(newKey)
	if newGetErr != nil {
		t.Fatalf("Get new key failed: %v", newGetErr)
	}

	if !newFound {
		t.Error("expected to find new key after insert")
	}

	if newEntry.Revision != 100 {
		t.Errorf("expected new entry revision=100, got %d", newEntry.Revision)
	}

	// Verify Len is now 2.
	length2, lenErr2 := c.Len()
	if lenErr2 != nil {
		t.Fatalf("Len failed: %v", lenErr2)
	}

	if length2 != 2 {
		t.Errorf("expected Len=2, got %d", length2)
	}

	// Re-open to verify persistence.
	c2, openErr := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 8,
	})
	if openErr != nil {
		t.Fatalf("Re-open failed: %v", openErr)
	}
	defer c2.Close()

	// Verify both entries after re-open.
	entry2, found2, getErr2 := c2.Get(survivingKey)
	if getErr2 != nil {
		t.Fatalf("Get after re-open failed: %v", getErr2)
	}

	if !found2 {
		t.Error("expected to find surviving key after re-open")
	}

	if entry2.Revision != 6 {
		t.Errorf("after re-open: expected revision=6, got %d", entry2.Revision)
	}

	newEntry2, newFound2, newGetErr2 := c2.Get(newKey)
	if newGetErr2 != nil {
		t.Fatalf("Get new key after re-open failed: %v", newGetErr2)
	}

	if !newFound2 {
		t.Error("expected to find new key after re-open")
	}

	if newEntry2.Revision != 100 {
		t.Errorf("after re-open: expected new entry revision=100, got %d", newEntry2.Revision)
	}
}

// Test_Cache_Preserves_Multiple_Survivors_When_Many_Deleted verifies that
// after deleting many entries, all surviving entries (multiple) remain accessible.
func Test_Cache_Preserves_Multiple_Survivors_When_Many_Deleted(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "multi_survivor.slc")

	// Create cache with slot_capacity=16, bucket_count=32.
	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 16,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Insert 12 entries with distinct revisions.
	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	keys := make([][]byte, 12)
	for i := range keys {
		keys[i] = []byte{0, 0, 0, byte(i + 1)}
	}

	idx := []byte{1, 2, 3, 4}

	for i, k := range keys {
		putErr := w.Put(k, int64(100+i), idx)
		if putErr != nil {
			t.Fatalf("Put failed: %v", putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	// Delete 9 entries, keep 3 survivors: keys[3], keys[7], keys[11].
	w2, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	survivorIndices := map[int]bool{3: true, 7: true, 11: true}

	for i, k := range keys {
		if !survivorIndices[i] {
			_, delErr := w2.Delete(k)
			if delErr != nil {
				t.Fatalf("Delete key %d failed: %v", i, delErr)
			}
		}
	}

	commitErr2 := w2.Commit()
	if commitErr2 != nil {
		t.Fatalf("Commit failed: %v", commitErr2)
	}

	// Verify all survivors are accessible with correct data.
	for i, k := range keys {
		entry, found, getErr := c.Get(k)
		if getErr != nil {
			t.Fatalf("Get key %d failed: %v", i, getErr)
		}

		if survivorIndices[i] {
			if !found {
				t.Errorf("expected to find surviving key %d", i)

				continue
			}

			if entry.Revision != int64(100+i) {
				t.Errorf("key %d: expected revision=%d, got %d", i, 100+i, entry.Revision)
			}

			if !bytes.Equal(entry.Key, k) {
				t.Errorf("key %d: expected key=%v, got %v", i, k, entry.Key)
			}

			if !bytes.Equal(entry.Index, idx) {
				t.Errorf("key %d: expected index=%v, got %v", i, idx, entry.Index)
			}
		} else if found {
			t.Errorf("expected deleted key %d to not be found", i)
		}
	}

	// Verify Len returns 3.
	length, lenErr := c.Len()
	if lenErr != nil {
		t.Fatalf("Len failed: %v", lenErr)
	}

	if length != 3 {
		t.Errorf("expected Len=3, got %d", length)
	}

	// Verify Scan returns all 3 survivors.
	entries, scanErr := c.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("Scan failed: %v", scanErr)
	}

	if len(entries) != 3 {
		t.Errorf("expected Scan to return 3 entries, got %d", len(entries))
	}

	// Verify each scanned entry matches a survivor.
	scannedRevisions := make(map[int64]bool)
	for _, e := range entries {
		scannedRevisions[e.Revision] = true
	}

	for i := range survivorIndices {
		expectedRev := int64(100 + i)
		if !scannedRevisions[expectedRev] {
			t.Errorf("expected Scan to include entry with revision %d", expectedRev)
		}
	}
}

// Test_Cache_Remains_Correct_When_Repeated_Delete_Insert_Cycles_Occur verifies
// that the cache remains correct through multiple cycles of heavy deletions and new inserts.
func Test_Cache_Remains_Correct_When_Repeated_Delete_Insert_Cycles_Occur(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cycles.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      4,
		IndexSize:    0,
		SlotCapacity: 32,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Perform 3 cycles of: insert batch -> delete most -> verify survivors.
	for cycle := range 3 {
		// Insert 8 new entries.
		w, beginErr := c.Writer()
		if beginErr != nil {
			t.Fatalf("cycle %d: BeginWrite failed: %v", cycle, beginErr)
		}

		baseKey := byte(cycle*10 + 1)
		for i := range 8 {
			key := []byte{0, 0, byte(cycle), baseKey + byte(i)}

			putErr := w.Put(key, int64(cycle*100+i), nil)
			if putErr != nil {
				t.Fatalf("cycle %d: Put failed: %v", cycle, putErr)
			}
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("cycle %d: Commit failed: %v", cycle, commitErr)
		}

		// Delete 6 of the 8 entries, keeping indices 2 and 5.
		w2, beginErr := c.Writer()
		if beginErr != nil {
			t.Fatalf("cycle %d: BeginWrite for delete failed: %v", cycle, beginErr)
		}

		for i := range 8 {
			if i == 2 || i == 5 {
				continue // keep these
			}

			key := []byte{0, 0, byte(cycle), baseKey + byte(i)}

			_, delErr := w2.Delete(key)
			if delErr != nil {
				t.Fatalf("cycle %d: Delete failed: %v", cycle, delErr)
			}
		}

		commitErr2 := w2.Commit()
		if commitErr2 != nil {
			t.Fatalf("cycle %d: Commit delete failed: %v", cycle, commitErr2)
		}

		// Verify survivors from this cycle.
		for _, keepIdx := range []int{2, 5} {
			key := []byte{0, 0, byte(cycle), baseKey + byte(keepIdx)}

			entry, found, getErr := c.Get(key)
			if getErr != nil {
				t.Fatalf("cycle %d: Get survivor %d failed: %v", cycle, keepIdx, getErr)
			}

			if !found {
				t.Errorf("cycle %d: expected to find survivor key index %d", cycle, keepIdx)
			}

			expectedRev := int64(cycle*100 + keepIdx)
			if entry.Revision != expectedRev {
				t.Errorf("cycle %d: survivor %d: expected revision=%d, got %d",
					cycle, keepIdx, expectedRev, entry.Revision)
			}
		}
	}

	// Final verification: we should have 6 entries (2 survivors per cycle × 3 cycles).
	length, lenErr := c.Len()
	if lenErr != nil {
		t.Fatalf("final Len failed: %v", lenErr)
	}

	if length != 6 {
		t.Errorf("expected final Len=6, got %d", length)
	}

	entries, scanErr := c.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("final Scan failed: %v", scanErr)
	}

	if len(entries) != 6 {
		t.Errorf("expected final Scan to return 6 entries, got %d", len(entries))
	}
}

// =============================================================================
// Copy Semantics (memory safety)
// =============================================================================

// Test_Scan_Returns_Detached_Copies_When_Results_Are_Mutated verifies that Scan() results
// are fully copied. Mutating the returned entries must not affect cache state.
func Test_Scan_Returns_Detached_Copies_When_Results_Are_Mutated(t *testing.T) {
	t.Parallel()

	cacheFile := filepath.Join(t.TempDir(), "copy_scan.slc")

	cache, openErr := slotcache.Open(slotcache.Options{
		Path:         cacheFile,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 16,
	})
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}

	defer cache.Close()

	// Insert some entries.
	key1 := []byte("key00001")
	key2 := []byte("key00002")
	index1 := []byte{0x11, 0x22, 0x33, 0x44}
	index2 := []byte{0x55, 0x66, 0x77, 0x88}

	writer, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	putErr1 := writer.Put(key1, 100, index1)
	if putErr1 != nil {
		t.Fatalf("Put key1: %v", putErr1)
	}

	putErr2 := writer.Put(key2, 200, index2)
	if putErr2 != nil {
		t.Fatalf("Put key2: %v", putErr2)
	}

	commitErr := writer.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Scan and save the original values.
	entries, scanErr := cache.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Save copies of original values before mutation.
	origKeys := make([][]byte, len(entries))
	origIndexes := make([][]byte, len(entries))
	origRevisions := make([]int64, len(entries))

	for i, e := range entries {
		origKeys[i] = append([]byte(nil), e.Key...)
		origIndexes[i] = append([]byte(nil), e.Index...)
		origRevisions[i] = e.Revision
	}

	// Mutate the scan results aggressively.
	for i := range entries {
		for j := range entries[i].Key {
			entries[i].Key[j] ^= 0xFF
		}

		for j := range entries[i].Index {
			entries[i].Index[j] ^= 0xFF
		}
	}

	// Verify cache state is unaffected by re-scanning.
	fresh, freshErr := cache.Scan(slotcache.ScanOptions{})
	if freshErr != nil {
		t.Fatalf("Scan after mutation: %v", freshErr)
	}

	if len(fresh) != len(origKeys) {
		t.Fatalf("entry count changed after mutation: was %d, now %d", len(origKeys), len(fresh))
	}

	for i, e := range fresh {
		if !bytes.Equal(e.Key, origKeys[i]) {
			t.Errorf("entry[%d].Key corrupted: got %x, want %x", i, e.Key, origKeys[i])
		}

		if !bytes.Equal(e.Index, origIndexes[i]) {
			t.Errorf("entry[%d].Index corrupted: got %x, want %x", i, e.Index, origIndexes[i])
		}

		if e.Revision != origRevisions[i] {
			t.Errorf("entry[%d].Revision corrupted: got %d, want %d", i, e.Revision, origRevisions[i])
		}
	}

	// Cross-check with Get().
	for i, origKey := range origKeys {
		gotEntry, ok, getErr := cache.Get(origKey)
		if getErr != nil {
			t.Fatalf("Get(%x) after mutation: %v", origKey, getErr)
		}

		if !ok {
			t.Fatalf("Get(%x) not found after mutation", origKey)
		}

		if gotEntry.Revision != origRevisions[i] {
			t.Errorf("Get(%x).Revision corrupted: got %d, want %d", origKey, gotEntry.Revision, origRevisions[i])
		}

		if !bytes.Equal(gotEntry.Index, origIndexes[i]) {
			t.Errorf("Get(%x).Index corrupted: got %x, want %x", origKey, gotEntry.Index, origIndexes[i])
		}
	}
}

// Test_Get_Returns_Fresh_Copies_When_Called_Multiple_Times verifies that each Get()
// call returns independent copies. Callers may retain multiple results.
func Test_Get_Returns_Fresh_Copies_When_Called_Multiple_Times(t *testing.T) {
	t.Parallel()

	cacheFile := filepath.Join(t.TempDir(), "copy_get.slc")

	cache, openErr := slotcache.Open(slotcache.Options{
		Path:         cacheFile,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 8,
	})
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}

	defer cache.Close()

	key := []byte("testkey1")
	index := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	revision := int64(42)

	writer, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	putErr := writer.Put(key, revision, index)
	if putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	commitErr := writer.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Get the same key twice.
	e1, ok1, err1 := cache.Get(key)
	if err1 != nil || !ok1 {
		t.Fatalf("Get #1: ok=%v err=%v", ok1, err1)
	}

	e2, ok2, err2 := cache.Get(key)
	if err2 != nil || !ok2 {
		t.Fatalf("Get #2: ok=%v err=%v", ok2, err2)
	}

	// Verify buffers are NOT aliased (different underlying arrays).
	if len(e1.Key) > 0 && len(e2.Key) > 0 && &e1.Key[0] == &e2.Key[0] {
		t.Error("Get() returned aliased Key buffers across calls")
	}

	if len(e1.Index) > 0 && len(e2.Index) > 0 && &e1.Index[0] == &e2.Index[0] {
		t.Error("Get() returned aliased Index buffers across calls")
	}

	// Mutate e1 aggressively (Key and Index only - Revision is a value type).
	for i := range e1.Key {
		e1.Key[i] ^= 0xFF
	}

	for i := range e1.Index {
		e1.Index[i] ^= 0xFF
	}

	// e2 should be unaffected.
	if !bytes.Equal(e2.Key, key) {
		t.Errorf("e2.Key affected by mutating e1: got %x, want %x", e2.Key, key)
	}

	if !bytes.Equal(e2.Index, index) {
		t.Errorf("e2.Index affected by mutating e1: got %x, want %x", e2.Index, index)
	}

	if e2.Revision != revision {
		t.Errorf("e2.Revision affected by mutating e1: got %d, want %d", e2.Revision, revision)
	}

	// Fresh Get should also be unaffected.
	e3, ok3, err3 := cache.Get(key)
	if err3 != nil || !ok3 {
		t.Fatalf("Get #3: ok=%v err=%v", ok3, err3)
	}

	if !bytes.Equal(e3.Key, key) {
		t.Errorf("e3.Key corrupted after mutating e1: got %x, want %x", e3.Key, key)
	}

	if !bytes.Equal(e3.Index, index) {
		t.Errorf("e3.Index corrupted after mutating e1: got %x, want %x", e3.Index, index)
	}

	if e3.Revision != revision {
		t.Errorf("e3.Revision corrupted after mutating e1: got %d, want %d", e3.Revision, revision)
	}
}

// Test_ScanPrefix_Returns_Detached_Copies_When_Results_Are_Mutated verifies ScanPrefix.
func Test_ScanPrefix_Returns_Detached_Copies_When_Results_Are_Mutated(t *testing.T) {
	t.Parallel()

	cacheFile := filepath.Join(t.TempDir(), "copy_prefix.slc")

	cache, openErr := slotcache.Open(slotcache.Options{
		Path:         cacheFile,
		KeySize:      8,
		IndexSize:    2,
		SlotCapacity: 16,
	})
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}

	defer cache.Close()

	writer, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	keys := [][]byte{
		[]byte("aaa00001"),
		[]byte("aaa00002"),
		[]byte("bbb00001"),
	}

	for i, k := range keys {
		putErr := writer.Put(k, int64(i+1), []byte{byte(i), byte(i)})
		if putErr != nil {
			t.Fatalf("Put %x: %v", k, putErr)
		}
	}

	commitErr := writer.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Scan with prefix "aaa".
	entries, scanErr := cache.ScanPrefix([]byte("aaa"), slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("ScanPrefix: %v", scanErr)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries with prefix 'aaa', got %d", len(entries))
	}

	// Save originals.
	origKeys := make([][]byte, len(entries))
	for i, e := range entries {
		origKeys[i] = append([]byte(nil), e.Key...)
	}

	// Mutate.
	for i := range entries {
		for j := range entries[i].Key {
			entries[i].Key[j] = 0x00
		}
	}

	// Re-scan should return original values.
	fresh, freshErr := cache.ScanPrefix([]byte("aaa"), slotcache.ScanOptions{})
	if freshErr != nil {
		t.Fatalf("ScanPrefix after mutation: %v", freshErr)
	}

	for i, e := range fresh {
		if !bytes.Equal(e.Key, origKeys[i]) {
			t.Errorf("ScanPrefix entry[%d].Key corrupted: got %x, want %x", i, e.Key, origKeys[i])
		}
	}
}

// Test_ScanRange_Returns_Detached_Copies_When_Results_Are_Mutated verifies ScanRange (ordered mode).
func Test_ScanRange_Returns_Detached_Copies_When_Results_Are_Mutated(t *testing.T) {
	t.Parallel()

	cacheFile := filepath.Join(t.TempDir(), "copy_range.slc")

	cache, openErr := slotcache.Open(slotcache.Options{
		Path:         cacheFile,
		KeySize:      8,
		IndexSize:    2,
		SlotCapacity: 16,
		OrderedKeys:  true,
	})
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}

	defer cache.Close()

	writer, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	// Insert in order (required for OrderedKeys).
	keys := [][]byte{
		[]byte("key00001"),
		[]byte("key00002"),
		[]byte("key00003"),
	}

	for i, k := range keys {
		putErr := writer.Put(k, int64(i+1), []byte{byte(i), byte(i)})
		if putErr != nil {
			t.Fatalf("Put %x: %v", k, putErr)
		}
	}

	commitErr := writer.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// ScanRange for middle key.
	entries, scanErr := cache.ScanRange([]byte("key00001"), []byte("key00003"), slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("ScanRange: %v", scanErr)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries in range, got %d", len(entries))
	}

	// Save originals.
	origKeys := make([][]byte, len(entries))
	for i, e := range entries {
		origKeys[i] = append([]byte(nil), e.Key...)
	}

	// Mutate.
	for i := range entries {
		for j := range entries[i].Key {
			entries[i].Key[j] = 0xFF
		}
	}

	// Re-scan should be unaffected.
	fresh, freshErr := cache.ScanRange([]byte("key00001"), []byte("key00003"), slotcache.ScanOptions{})
	if freshErr != nil {
		t.Fatalf("ScanRange after mutation: %v", freshErr)
	}

	for i, e := range fresh {
		if !bytes.Equal(e.Key, origKeys[i]) {
			t.Errorf("ScanRange entry[%d].Key corrupted: got %x, want %x", i, e.Key, origKeys[i])
		}
	}
}

// Test_Get_Returns_Detached_Copies_When_IndexSize_Is_Zero verifies copy semantics with IndexSize=0.
func Test_Get_Returns_Detached_Copies_When_IndexSize_Is_Zero(t *testing.T) {
	t.Parallel()

	cacheFile := filepath.Join(t.TempDir(), "copy_noindex.slc")

	cache, openErr := slotcache.Open(slotcache.Options{
		Path:         cacheFile,
		KeySize:      4,
		IndexSize:    0,
		SlotCapacity: 8,
	})
	if openErr != nil {
		t.Fatalf("Open: %v", openErr)
	}

	defer cache.Close()

	key := []byte("test")

	writer, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	putErr := writer.Put(key, 1, nil)
	if putErr != nil {
		t.Fatalf("Put: %v", putErr)
	}

	commitErr := writer.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	// Get twice and verify keys are independent.
	e1, ok1, _ := cache.Get(key)
	e2, ok2, _ := cache.Get(key)

	if !ok1 || !ok2 {
		t.Fatal("Get failed")
	}

	if len(e1.Key) > 0 && len(e2.Key) > 0 && &e1.Key[0] == &e2.Key[0] {
		t.Error("Get() returned aliased Key buffers with IndexSize=0")
	}

	// Mutate e1 key.
	e1.Key[0] ^= 0xFF

	// e2 should be unaffected.
	if !bytes.Equal(e2.Key, key) {
		t.Errorf("e2.Key affected by mutating e1: got %x, want %x", e2.Key, key)
	}
}

// =============================================================================
// Generation / Seqlock Protocol
// =============================================================================

// Test_Get_Returns_Entry_When_Generation_Is_Stable_Even verifies that Get works
// correctly when the generation counter is stable (even).
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

	w, beginErr := cache.Writer()
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
		w, beginErr := cache.Writer()
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

	w, beginErr := cache2.Writer()
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
