// ScanRange corruption tests.
//
// These tests verify that ScanRange() behaves correctly when the ordered-keys
// invariant is violated by file corruption. Per spec (001-overview.md):
// "Sound reads (no false positives): read APIs MUST NOT return an entry unless
// the returned key bytes match the requested key under a stable even generation."
//
// For ScanRange, this means we must not return keys outside [start, end).
// If the file is corrupted such that keys are not actually sorted (despite
// FLAG_ORDERED_KEYS being set), ScanRange should either:
// - Return ErrCorrupt (ideal), or
// - At minimum, not return keys outside the requested range
//
// Why this matters:
// ScanRange uses binary search to find the start position, assuming keys are
// sorted. If keys are out of order, binary search may land at the wrong slot,
// potentially causing keys < start to be returned. This would violate the
// "no false positives" guarantee.

package slotcache_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Test_ScanRange_Does_Not_Return_Keys_Below_Start_When_Ordered_Invariant_Violated
// verifies that ScanRange() doesn't return keys < start when the file is corrupted
// with out-of-order keys (despite FLAG_ORDERED_KEYS being set).
//
// Corruption scenario: We create a "reverse sorted" corruption where binary search
// lands at slot 0 (because early slots have large keys), then iteration proceeds
// through later slots that have keys < start.
//
// Original: ["aaaaaaaa", "bbbbbbbb", "cccccccc"]
// Corrupted: ["xxxxxxxx", "yyyyyyyy", "aaaaaaaa"]
// Query: start="bbbbbbbb", end=nil
//
// Binary search for "bbbbbbbb" on corrupted data:
//   - mid=1, key="yyyyyyyy" >= "bbbbbbbb", high=1
//   - mid=0, key="xxxxxxxx" >= "bbbbbbbb", high=0
//   - startSlot=0
//
// Iteration from slot 0 with no end bound returns all keys including "aaaaaaaa",
// which is < "bbbbbbbb" (false positive).
func Test_ScanRange_Does_Not_Return_Keys_Below_Start_When_Ordered_Invariant_Violated(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_corrupt_order.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		OrderedKeys:    true,
		DisableLocking: true,
	}

	// Create cache with sorted keys.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	// Insert keys in sorted order: aaa < bbb < ccc
	errPut := w.Put([]byte("aaaaaaaa"), 1, make([]byte, 4))
	if errPut != nil {
		t.Fatalf("Put(aaa) failed: %v", errPut)
	}

	errPut = w.Put([]byte("bbbbbbbb"), 2, make([]byte, 4))
	if errPut != nil {
		t.Fatalf("Put(bbb) failed: %v", errPut)
	}

	errPut = w.Put([]byte("cccccccc"), 3, make([]byte, 4))
	if errPut != nil {
		t.Fatalf("Put(ccc) failed: %v", errPut)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt to reverse order: ["xxxxxxxx", "yyyyyyyy", "aaaaaaaa"]
	// This causes binary search to land at slot 0 for query start="bbbbbbbb",
	// and iteration will include slot 2's "aaaaaaaa" which is < start.
	mutateFile(t, path, func(data []byte) {
		slotSize := 32 // slot size for key_size=8, index_size=4

		// Slot 0
		slot0KeyOffset := slcHeaderSize + 8
		copy(data[slot0KeyOffset:slot0KeyOffset+8], "xxxxxxxx")

		// Slot 1
		slot1KeyOffset := slcHeaderSize + slotSize + 8
		copy(data[slot1KeyOffset:slot1KeyOffset+8], "yyyyyyyy")

		// Slot 2
		slot2KeyOffset := slcHeaderSize + 2*slotSize + 8
		copy(data[slot2KeyOffset:slot2KeyOffset+8], "aaaaaaaa")
	})

	// Reopen and query range ["bbbbbbbb", nil).
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	start := []byte("bbbbbbbb")
	entries, err := cache2.ScanRange(start, nil, slotcache.ScanOptions{})

	// Acceptable outcomes:
	// 1. ErrCorrupt (detected the invariant violation)
	// 2. No error, but no keys < start returned
	if err != nil {
		if !errors.Is(err, slotcache.ErrCorrupt) && !errors.Is(err, slotcache.ErrBusy) {
			t.Fatalf("ScanRange returned unexpected error: %v", err)
		}
		// ErrCorrupt or ErrBusy is acceptable
		return
	}

	// No error - verify no keys below start were returned.
	for _, e := range entries {
		if bytes.Compare(e.Key, start) < 0 {
			t.Errorf("ScanRange returned key %q which is < start bound %q (false positive)",
				e.Key, start)
		}
	}
}

// Test_ScanRange_Does_Not_Return_Keys_Above_End_When_Ordered_Invariant_Violated
// verifies that ScanRange() doesn't return keys >= end when the file is corrupted.
// This is a complementary test - the end bound check already exists, but we verify
// it still works under corruption.
func Test_ScanRange_Does_Not_Return_Keys_Above_End_When_Ordered_Invariant_Violated(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_corrupt_order_end.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		OrderedKeys:    true,
		DisableLocking: true,
	}

	// Create cache with sorted keys.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	// Insert keys in sorted order
	errPut := w.Put([]byte("aaaaaaaa"), 1, make([]byte, 4))
	if errPut != nil {
		t.Fatalf("Put(aaa) failed: %v", errPut)
	}

	errPut = w.Put([]byte("bbbbbbbb"), 2, make([]byte, 4))
	if errPut != nil {
		t.Fatalf("Put(bbb) failed: %v", errPut)
	}

	errPut = w.Put([]byte("cccccccc"), 3, make([]byte, 4))
	if errPut != nil {
		t.Fatalf("Put(ccc) failed: %v", errPut)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt: change slot 2's key from "cccccccc" to "aaaaaaaa".
	// This creates duplicates and breaks order for iteration.
	mutateFile(t, path, func(data []byte) {
		// slot_size for key_size=8, index_size=4: align8(8+8+0+8+4) = 32
		slotSize := 32
		slot2KeyOffset := slcHeaderSize + 2*slotSize + 8
		copy(data[slot2KeyOffset:slot2KeyOffset+8], "aaaaaaaa")
	})

	// Reopen and query range [nil, "bbbbbbbb").
	// Should return only keys < "bbbbbbbb".
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	end := []byte("bbbbbbbb")

	entries, err := cache2.ScanRange(nil, end, slotcache.ScanOptions{})
	if err != nil {
		if !errors.Is(err, slotcache.ErrCorrupt) && !errors.Is(err, slotcache.ErrBusy) {
			t.Fatalf("ScanRange returned unexpected error: %v", err)
		}

		return
	}

	// Verify no keys >= end were returned.
	for _, e := range entries {
		if bytes.Compare(e.Key, end) >= 0 {
			t.Errorf("ScanRange returned key %q which is >= end bound %q (false positive)",
				e.Key, end)
		}
	}
}

// Test_ScanRange_Respects_Both_Bounds_When_Ordered_Invariant_Violated verifies
// behavior when both start and end bounds are specified and the file has out-of-order keys.
func Test_ScanRange_Respects_Both_Bounds_When_Ordered_Invariant_Violated(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_corrupt_both.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		OrderedKeys:    true,
		DisableLocking: true,
	}

	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	// Insert 5 keys in sorted order
	keys := []string{"aaaaaaaa", "bbbbbbbb", "cccccccc", "dddddddd", "eeeeeeee"}
	for i, k := range keys {
		err := w.Put([]byte(k), int64(i+1), make([]byte, 4))
		if err != nil {
			t.Fatalf("Put(%s) failed: %v", k, err)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt: swap slot 1 ("bbbbbbbb") with "xxxxxxxx" and slot 3 ("dddddddd") with "aaaaaaaa"
	// Result: ["aaaaaaaa", "xxxxxxxx", "cccccccc", "aaaaaaaa", "eeeeeeee"]
	mutateFile(t, path, func(data []byte) {
		slotSize := 32

		// Slot 1
		slot1KeyOffset := slcHeaderSize + 1*slotSize + 8
		copy(data[slot1KeyOffset:slot1KeyOffset+8], "xxxxxxxx")

		// Slot 3
		slot3KeyOffset := slcHeaderSize + 3*slotSize + 8
		copy(data[slot3KeyOffset:slot3KeyOffset+8], "aaaaaaaa")
	})

	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Reopen failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	// Query range ["cccccccc", "eeeeeeee")
	start := []byte("cccccccc")
	end := []byte("eeeeeeee")

	entries, err := cache2.ScanRange(start, end, slotcache.ScanOptions{})
	if err != nil {
		if !errors.Is(err, slotcache.ErrCorrupt) && !errors.Is(err, slotcache.ErrBusy) {
			t.Fatalf("ScanRange returned unexpected error: %v", err)
		}

		return
	}

	// Verify all returned keys are within [start, end).
	for _, e := range entries {
		if bytes.Compare(e.Key, start) < 0 {
			t.Errorf("ScanRange returned key %q < start %q", e.Key, start)
		}

		if bytes.Compare(e.Key, end) >= 0 {
			t.Errorf("ScanRange returned key %q >= end %q", e.Key, end)
		}
	}
}
