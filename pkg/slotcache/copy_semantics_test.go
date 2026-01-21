// Copy semantics regression test
//
// This test validates that slotcache returns detached copies of data,
// not borrowed references to mmap memory. Callers must be able to retain
// and mutate returned Entry slices without corrupting cache state.
//
// This is tested separately from CompareState to avoid running expensive
// copy-semantics checks on every heavy comparison during fuzz/property tests.

package slotcache_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

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

	writer, beginErr := cache.BeginWrite()
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
		// Note: We don't mutate Revision here since it's a value type (int64),
		// not a slice, so mutation doesn't test memory aliasing.
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

	writer, beginErr := cache.BeginWrite()
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

	writer, beginErr := cache.BeginWrite()
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

	writer, beginErr := cache.BeginWrite()
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

	writer, beginErr := cache.BeginWrite()
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
