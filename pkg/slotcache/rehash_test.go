package slotcache_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

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
	w, err := c.BeginWrite()
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
	w2, beginErr := c.BeginWrite()
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
	w3, beginErr := c.BeginWrite()
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
	w, err := c.BeginWrite()
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
	w2, beginErr := c.BeginWrite()
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
		w, beginErr := c.BeginWrite()
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
		w2, beginErr := c.BeginWrite()
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
