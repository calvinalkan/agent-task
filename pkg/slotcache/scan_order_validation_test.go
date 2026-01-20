// Scan order validation: unit tests for ordered-keys mode order checking during scans
//
// Oracle: ErrCorrupt when keys are out of order in ordered-keys mode
// Technique: direct file corruption of slot key bytes + scan
//
// These tests verify that scan operations in ordered-keys mode validate the
// sorted key invariant during iteration. If keys are found out of order,
// the scan must return ErrCorrupt (not silently return wrong results).
//
// Why this matters:
// - The sorted invariant (slot[i].key <= slot[j].key for i < j) is fundamental
//   to ordered-keys mode correctness
// - Without runtime validation, corruption could cause silent data issues:
//   - Binary search could land on wrong entries
//   - Range scans could miss entries or return duplicates
//   - Early termination could exit prematurely or too late
// - Runtime validation catches corruption that Open() sampling might miss
//
// Failures here mean: "Scan accepted out-of-order keys without detecting corruption"

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

// Test_Scan_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order verifies that
// a forward scan in ordered-keys mode detects out-of-order keys and returns ErrCorrupt.
func Test_Scan_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_order_corrupt.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create a valid ordered cache with sorted entries.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys in sorted order: 0x0100, 0x0200, 0x0300, 0x0400
	keys := []uint64{0x0100, 0x0200, 0x0300, 0x0400}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Verify scan works before corruption.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(reopen before corruption) failed: %v", openErr)
	}

	entries, scanErr := c2.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("Scan(before corruption) failed: %v", scanErr)
	}

	if len(entries) != 4 {
		t.Fatalf("Scan(before corruption): got %d entries, want 4", len(entries))
	}

	closeErr = c2.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// Corrupt slot 1's key to be less than slot 0's key (violate sorted invariant).
	// Slot 0 has key 0x0100, Slot 1 has key 0x0200.
	// We'll change Slot 1's key to 0x0050 (which is < 0x0100).
	corruptSlotKey(t, path, 1, 0x0050)

	// Reopen and scan - should detect corruption.
	c3, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(after corruption) failed: %v", openErr)
	}
	defer c3.Close()

	_, scanErr = c3.Scan(slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrCorrupt) {
		t.Fatalf("Scan(corrupted order) error mismatch: got=%v want=%v", scanErr, slotcache.ErrCorrupt)
	}
}

// Test_Scan_Reverse_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order verifies that
// a reverse scan in ordered-keys mode detects out-of-order keys and returns ErrCorrupt.
func Test_Scan_Reverse_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_reverse_order_corrupt.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create a valid ordered cache.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys in sorted order.
	keys := []uint64{0x0100, 0x0200, 0x0300, 0x0400}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Corrupt slot 2's key to be greater than slot 3's key.
	// Slot 2 has key 0x0300, Slot 3 has key 0x0400.
	// Change Slot 2's key to 0x0500 (which is > 0x0400).
	// When iterating backwards (slot 3 -> 2), we expect key to decrease.
	corruptSlotKey(t, path, 2, 0x0500)

	// Reopen and reverse scan - should detect corruption.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(after corruption) failed: %v", openErr)
	}
	defer c2.Close()

	_, scanErr := c2.Scan(slotcache.ScanOptions{Reverse: true})
	if !errors.Is(scanErr, slotcache.ErrCorrupt) {
		t.Fatalf("Scan(Reverse, corrupted order) error mismatch: got=%v want=%v", scanErr, slotcache.ErrCorrupt)
	}
}

// Test_ScanRange_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order verifies that
// ScanRange detects out-of-order keys within the scanned range.
func Test_ScanRange_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_order_corrupt.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create a valid ordered cache with more entries.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys: 0x0100, 0x0200, 0x0300, 0x0400, 0x0500, 0x0600
	keys := []uint64{0x0100, 0x0200, 0x0300, 0x0400, 0x0500, 0x0600}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Corrupt slot 3 (key 0x0400) to be less than slot 2 (key 0x0300).
	// Change slot 3's key to 0x0250.
	corruptSlotKey(t, path, 3, 0x0250)

	// Reopen and range scan that covers the corrupted area.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(after corruption) failed: %v", openErr)
	}
	defer c2.Close()

	// Scan range [0x0200, 0x0500) which should include slots 1, 2, 3, 4.
	startKey := make([]byte, 8)
	binary.BigEndian.PutUint64(startKey, 0x0200)

	endKey := make([]byte, 8)
	binary.BigEndian.PutUint64(endKey, 0x0500)

	_, scanErr := c2.ScanRange(startKey, endKey, slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrCorrupt) {
		t.Fatalf("ScanRange(corrupted order) error mismatch: got=%v want=%v", scanErr, slotcache.ErrCorrupt)
	}
}

// Test_ScanRange_Reverse_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order verifies that
// reverse ScanRange detects out-of-order keys.
func Test_ScanRange_Reverse_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_reverse_order_corrupt.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create a valid ordered cache.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys in sorted order.
	keys := []uint64{0x0100, 0x0200, 0x0300, 0x0400, 0x0500, 0x0600}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Corrupt slot 2's key to be greater than slot 3's key.
	// Slot 2 has key 0x0300, Slot 3 has key 0x0400.
	// Change slot 2's key to 0x0450 (which is > 0x0400).
	corruptSlotKey(t, path, 2, 0x0450)

	// Reopen and reverse range scan.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(after corruption) failed: %v", openErr)
	}
	defer c2.Close()

	// Scan range [0x0200, 0x0600) in reverse.
	startKey := make([]byte, 8)
	binary.BigEndian.PutUint64(startKey, 0x0200)

	endKey := make([]byte, 8)
	binary.BigEndian.PutUint64(endKey, 0x0600)

	_, scanErr := c2.ScanRange(startKey, endKey, slotcache.ScanOptions{Reverse: true})
	if !errors.Is(scanErr, slotcache.ErrCorrupt) {
		t.Fatalf("ScanRange(Reverse, corrupted order) error mismatch: got=%v want=%v", scanErr, slotcache.ErrCorrupt)
	}
}

// Test_Scan_Succeeds_When_Ordered_Keys_Are_Valid verifies that scans work normally
// when the sorted invariant is maintained (no false positives).
func Test_Scan_Succeeds_When_Ordered_Keys_Are_Valid(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_order_valid.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create a valid ordered cache.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys in strictly sorted order.
	keys := []uint64{0x0100, 0x0200, 0x0300, 0x0400}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Reopen and verify all scan variants work.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(reopen) failed: %v", openErr)
	}
	defer c2.Close()

	// Forward scan.
	entries, scanErr := c2.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("Scan(forward) failed: %v", scanErr)
	}

	if len(entries) != 4 {
		t.Fatalf("Scan(forward): got %d entries, want 4", len(entries))
	}

	// Verify order is correct.
	for i := 1; i < len(entries); i++ {
		if bytes.Compare(entries[i-1].Key, entries[i].Key) > 0 {
			t.Fatalf("Scan(forward): keys out of order at index %d", i)
		}
	}

	// Reverse scan.
	entriesRev, scanErr := c2.Scan(slotcache.ScanOptions{Reverse: true})
	if scanErr != nil {
		t.Fatalf("Scan(reverse) failed: %v", scanErr)
	}

	if len(entriesRev) != 4 {
		t.Fatalf("Scan(reverse): got %d entries, want 4", len(entriesRev))
	}

	// Verify reverse order is correct (descending).
	for i := 1; i < len(entriesRev); i++ {
		if bytes.Compare(entriesRev[i-1].Key, entriesRev[i].Key) < 0 {
			t.Fatalf("Scan(reverse): keys not in descending order at index %d", i)
		}
	}

	// Range scan.
	startKey := make([]byte, 8)
	binary.BigEndian.PutUint64(startKey, 0x0200)

	endKey := make([]byte, 8)
	binary.BigEndian.PutUint64(endKey, 0x0400)

	rangeEntries, scanErr := c2.ScanRange(startKey, endKey, slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("ScanRange(forward) failed: %v", scanErr)
	}

	if len(rangeEntries) != 2 { // 0x0200, 0x0300
		t.Fatalf("ScanRange(forward): got %d entries, want 2", len(rangeEntries))
	}

	// Reverse range scan.
	rangeEntriesRev, scanErr := c2.ScanRange(startKey, endKey, slotcache.ScanOptions{Reverse: true})
	if scanErr != nil {
		t.Fatalf("ScanRange(reverse) failed: %v", scanErr)
	}

	if len(rangeEntriesRev) != 2 {
		t.Fatalf("ScanRange(reverse): got %d entries, want 2", len(rangeEntriesRev))
	}
}

// Test_Scan_Does_Not_Validate_Order_When_Unordered_Mode verifies that scans in
// non-ordered-keys mode do NOT perform order validation (no false positives).
func Test_Scan_Does_Not_Validate_Order_When_Unordered_Mode(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_unordered_mode.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  false, // Unordered mode!
	}

	// Create a cache in unordered mode.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// In unordered mode, we can insert keys in any order.
	// Insert out-of-order keys: 0x0400, 0x0100, 0x0300, 0x0200
	keys := []uint64{0x0400, 0x0100, 0x0300, 0x0200}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Reopen and scan - should succeed even though keys are not sorted.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(reopen) failed: %v", openErr)
	}
	defer c2.Close()

	entries, scanErr := c2.Scan(slotcache.ScanOptions{})
	if scanErr != nil {
		t.Fatalf("Scan(unordered mode) failed: %v", scanErr)
	}

	if len(entries) != 4 {
		t.Fatalf("Scan(unordered mode): got %d entries, want 4", len(entries))
	}

	// Keys should be in insertion order, not sorted order.
	expectedOrder := []uint64{0x0400, 0x0100, 0x0300, 0x0200}

	for i, entry := range entries {
		keyVal := binary.BigEndian.Uint64(entry.Key)
		if keyVal != expectedOrder[i] {
			t.Fatalf("Scan(unordered mode): entry %d key mismatch: got=0x%04X want=0x%04X",
				i, keyVal, expectedOrder[i])
		}
	}
}

// Test_Scan_Returns_ErrCorrupt_When_Keys_Out_Of_Order_With_Tombstones verifies that order validation
// works correctly when there are tombstoned (deleted) entries.
func Test_Scan_Returns_ErrCorrupt_When_Keys_Out_Of_Order_With_Tombstones(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_order_tombstones.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create cache and insert entries.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys: 0x0100, 0x0200, 0x0300, 0x0400, 0x0500
	keys := []uint64{0x0100, 0x0200, 0x0300, 0x0400, 0x0500}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

		putErr := w.Put(key, int64(i), index)
		if putErr != nil {
			t.Fatalf("Put(%d) failed: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	// Delete middle entries to create tombstones.
	w2, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite(delete) failed: %v", err)
	}

	// Delete 0x0200 and 0x0400.
	for _, keyVal := range []uint64{0x0200, 0x0400} {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		_, delErr := w2.Delete(key)
		if delErr != nil {
			t.Fatalf("Delete(0x%04X) failed: %v", keyVal, delErr)
		}
	}

	commitErr = w2.Commit()
	if commitErr != nil {
		t.Fatalf("Commit(delete) failed: %v", commitErr)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// Corrupt a live slot to violate order.
	// Slot 2 (key 0x0300, live) will be changed to 0x0050.
	corruptSlotKey(t, path, 2, 0x0050)

	// Reopen and scan - should detect corruption.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(after corruption) failed: %v", openErr)
	}
	defer c2.Close()

	_, scanErr := c2.Scan(slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrCorrupt) {
		t.Fatalf("Scan(with tombstones, corrupted order) error mismatch: got=%v want=%v",
			scanErr, slotcache.ErrCorrupt)
	}
}

// Test_ScanPrefix_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order verifies that ScanPrefix also validates order
// in ordered-keys mode.
func Test_ScanPrefix_Returns_ErrCorrupt_When_Ordered_Keys_Are_Out_Of_Order(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanprefix_order.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 16,
		OrderedKeys:  true,
	}

	// Create cache with sorted keys.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	// Insert keys with common prefix: 0xAA01, 0xAA02, 0xAA03, 0xAA04
	keys := []uint64{0xAA01000000000000, 0xAA02000000000000, 0xAA03000000000000, 0xAA04000000000000}
	for i, keyVal := range keys {
		key := make([]byte, 8)
		binary.BigEndian.PutUint64(key, keyVal)

		index := make([]byte, 4)
		binary.BigEndian.PutUint32(index, uint32(i))

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

	// Corrupt slot 2 to violate order.
	corruptSlotKey(t, path, 2, 0xAA00000000000000) // Less than slot 1

	// Reopen and prefix scan.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(after corruption) failed: %v", openErr)
	}
	defer c2.Close()

	_, scanErr := c2.ScanPrefix([]byte{0xAA}, slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrCorrupt) {
		t.Fatalf("ScanPrefix(corrupted order) error mismatch: got=%v want=%v",
			scanErr, slotcache.ErrCorrupt)
	}
}

// corruptSlotKey directly modifies a slot's key bytes in the file.
// This is used to create out-of-order key corruption for testing.
// All tests in this file use 8-byte keys, so keySize is hardcoded.
func corruptSlotKey(t *testing.T, path string, slotID uint64, newKeyValue uint64) {
	t.Helper()

	const keySize = 8

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile failed: %v", err)
	}
	defer f.Close()

	// Read header to get slots offset and slot size.
	headerBuf := make([]byte, 256)

	_, readErr := f.ReadAt(headerBuf, 0)
	if readErr != nil {
		t.Fatalf("ReadAt(header) failed: %v", readErr)
	}

	slotsOffset := binary.LittleEndian.Uint64(headerBuf[0x060:])
	slotSize := binary.LittleEndian.Uint32(headerBuf[0x014:])

	// Slot layout: meta(8) + key(keySize) + padding + revision(8) + index
	// Key starts at offset 8 within the slot.
	slotOffset := slotsOffset + slotID*uint64(slotSize)
	keyOffset := slotOffset + 8 // Skip meta

	// Write new key value.
	keyBuf := make([]byte, keySize)
	binary.BigEndian.PutUint64(keyBuf, newKeyValue)

	_, writeErr := f.WriteAt(keyBuf, int64(keyOffset))
	if writeErr != nil {
		t.Fatalf("WriteAt(corrupt key) failed: %v", writeErr)
	}

	t.Logf("Corrupted slot %d key to 0x%016X", slotID, newKeyValue)
}
