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

// Test_Scan_Returns_Correct_Entries_When_Forward_With_Limit verifies that forward scans with
// Limit return the correct results even with early termination optimization.
// This test ensures the optimization doesn't break correctness.
func Test_Scan_Returns_Correct_Entries_When_Forward_With_Limit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "early_term.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert 10 entries in order.
	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	for i := range 10 {
		key := []byte{0, 0, 0, byte(i)}
		idx := []byte{byte(i), 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Test 1: Forward scan with Limit=3 should return first 3 entries.
	entries, err := c.Scan(slotcache.ScanOptions{Limit: 3})
	if err != nil {
		t.Fatalf("Scan with Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(i)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}

	// Test 2: Forward scan with Offset=2, Limit=3 should return entries 2,3,4.
	entries, err = c.Scan(slotcache.ScanOptions{Offset: 2, Limit: 3})
	if err != nil {
		t.Fatalf("Scan with Offset+Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(i + 2)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}

	// Test 3: Forward scan with large Offset should return fewer entries.
	entries, err = c.Scan(slotcache.ScanOptions{Offset: 8, Limit: 5})
	if err != nil {
		t.Fatalf("Scan with large Offset: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	// Test 4: Reverse scan with Limit=3 should return last 3 entries in reverse.
	entries, err = c.Scan(slotcache.ScanOptions{Reverse: true, Limit: 3})
	if err != nil {
		t.Fatalf("Reverse Scan with Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	// In reverse, we should get entries 9, 8, 7.
	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(9 - i)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}

	// Test 5: Reverse scan with Offset=2, Limit=3 should skip first 2 reversed entries.
	entries, err = c.Scan(slotcache.ScanOptions{Reverse: true, Offset: 2, Limit: 3})
	if err != nil {
		t.Fatalf("Reverse Scan with Offset+Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	// In reverse with Offset=2: skip 9,8 -> return 7,6,5.
	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(7 - i)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}
}

// Test_ScanRange_Returns_Correct_Entries_When_Forward_With_Limit verifies that range scans
// with Limit return correct results even with early termination optimization.
func Test_ScanRange_Returns_Correct_Entries_When_Forward_With_Limit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "range_early_term.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert 10 entries in sorted order (required for OrderedKeys).
	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	for i := range 10 {
		key := []byte{0, 0, 0, byte(i)}
		idx := []byte{byte(i), 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Test 1: Range scan with Limit=3 should return first 3 entries.
	entries, err := c.ScanRange(nil, nil, slotcache.ScanOptions{Limit: 3})
	if err != nil {
		t.Fatalf("ScanRange with Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(i)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}

	// Test 2: Range scan with bounded range and Limit.
	start := []byte{0, 0, 0, 2}
	end := []byte{0, 0, 0, 8}

	entries, err = c.ScanRange(start, end, slotcache.ScanOptions{Limit: 3})
	if err != nil {
		t.Fatalf("ScanRange bounded with Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	// Should be entries 2, 3, 4.
	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(i + 2)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}

	// Test 3: Range scan with Offset+Limit.
	entries, err = c.ScanRange(start, end, slotcache.ScanOptions{Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("ScanRange with Offset+Limit: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
	// Range is [2,8), so entries 2,3,4,5,6,7. Offset=2 skips 2,3 -> returns 4,5.
	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(i + 4)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}

	// Test 4: Reverse range scan with Limit.
	entries, err = c.ScanRange(start, end, slotcache.ScanOptions{Reverse: true, Limit: 3})
	if err != nil {
		t.Fatalf("Reverse ScanRange with Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}
	// Range is [2,8), reversed -> 7,6,5,4,3,2. Limit=3 -> 7,6,5.
	for i, e := range entries {
		expected := []byte{0, 0, 0, byte(7 - i)}
		if !bytes.Equal(e.Key, expected) {
			t.Errorf("entry %d: expected key %v, got %v", i, expected, e.Key)
		}
	}
}

// Test_Scan_Returns_Correct_Entries_When_Filter_And_Limit_Combined verifies that filtering combined with Limit
// works correctly (filter is applied before counting for Limit).
func Test_Scan_Returns_Correct_Entries_When_Filter_And_Limit_Combined(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "filter_limit.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert 10 entries, alternating "even" and "odd" markers in index.
	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	for i := range 10 {
		key := []byte{0, 0, 0, byte(i)}
		marker := byte(i % 2) // 0 for even, 1 for odd
		idx := []byte{marker, 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Filter that only keeps "even" entries (index[0] == 0).
	evenFilter := func(e slotcache.Entry) bool {
		return e.Index[0] == 0
	}

	// Test: Forward scan with filter and Limit=2.
	// Even entries are: 0, 2, 4, 6, 8. With Limit=2: 0, 2.
	entries, err := c.Scan(slotcache.ScanOptions{Filter: evenFilter, Limit: 2})
	if err != nil {
		t.Fatalf("Scan with filter and Limit: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	expected := []int{0, 2}
	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expected[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("entry %d: expected key %v, got %v", i, exp, e.Key)
		}
	}

	// Test: Forward scan with filter, Offset=2, Limit=2.
	// Even entries: 0, 2, 4, 6, 8. Skip first 2 -> 4, 6. Limit=2 -> 4, 6.
	entries, err = c.Scan(slotcache.ScanOptions{Filter: evenFilter, Offset: 2, Limit: 2})
	if err != nil {
		t.Fatalf("Scan with filter, Offset, and Limit: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}

	expected = []int{4, 6}
	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expected[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("entry %d: expected key %v, got %v", i, exp, e.Key)
		}
	}
}

// Test_ScanPrefix_Returns_Correct_Results_When_Using_Range_Optimization_In_OrderedKeys_Mode verifies that ScanPrefix
// in ordered-keys mode uses binary search acceleration and returns correct results.
// This tests the Phase 10.4 optimization.
func Test_ScanPrefix_Returns_Correct_Results_When_Using_Range_Optimization_In_OrderedKeys_Mode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "prefix_range.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert entries with different prefixes in sorted order.
	// Keys: 0x00..., 0x01..., 0x02..., 0xAA00..., 0xAA01..., 0xAA02..., 0xBB...
	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	keys := [][]byte{
		{0x00, 0x00, 0x00, 0x00},
		{0x01, 0x00, 0x00, 0x00},
		{0x02, 0x00, 0x00, 0x00},
		{0xAA, 0x00, 0x00, 0x00},
		{0xAA, 0x01, 0x00, 0x00},
		{0xAA, 0x02, 0x00, 0x00},
		{0xBB, 0x00, 0x00, 0x00},
	}

	for i, key := range keys {
		idx := []byte{byte(i), 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Test 1: ScanPrefix for 0xAA should return 3 entries.
	entries, err := c.ScanPrefix([]byte{0xAA}, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("ScanPrefix(0xAA): %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("ScanPrefix(0xAA): expected 3 entries, got %d", len(entries))
	}

	for _, e := range entries {
		if e.Key[0] != 0xAA {
			t.Errorf("ScanPrefix(0xAA): unexpected key prefix %x", e.Key)
		}
	}

	// Test 2: ScanPrefix for 0xAA with Limit=2.
	entries, err = c.ScanPrefix([]byte{0xAA}, slotcache.ScanOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ScanPrefix(0xAA, Limit=2): %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("ScanPrefix(0xAA, Limit=2): expected 2 entries, got %d", len(entries))
	}

	// Test 3: ScanPrefix for 0xAA with Reverse.
	entries, err = c.ScanPrefix([]byte{0xAA}, slotcache.ScanOptions{Reverse: true})
	if err != nil {
		t.Fatalf("ScanPrefix(0xAA, Reverse): %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("ScanPrefix(0xAA, Reverse): expected 3 entries, got %d", len(entries))
	}

	// Verify reverse order: 0xAA02, 0xAA01, 0xAA00.
	expectedReverse := [][]byte{
		{0xAA, 0x02, 0x00, 0x00},
		{0xAA, 0x01, 0x00, 0x00},
		{0xAA, 0x00, 0x00, 0x00},
	}

	for i, e := range entries {
		if !bytes.Equal(e.Key, expectedReverse[i]) {
			t.Errorf("ScanPrefix(0xAA, Reverse) entry %d: expected %x, got %x", i, expectedReverse[i], e.Key)
		}
	}

	// Test 4: ScanPrefix for non-existent prefix should return empty.
	entries, err = c.ScanPrefix([]byte{0xCC}, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("ScanPrefix(0xCC): %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("ScanPrefix(0xCC): expected 0 entries, got %d", len(entries))
	}

	// Test 5: ScanPrefix for 0x00 should return only 1 entry.
	entries, err = c.ScanPrefix([]byte{0x00}, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("ScanPrefix(0x00): %v", err)
	}

	if len(entries) != 1 {
		t.Errorf("ScanPrefix(0x00): expected 1 entry, got %d", len(entries))
	}
}

// Test_ScanPrefix_Returns_All_Matching_Entries_When_Prefix_Is_AllFF verifies that ScanPrefix handles
// the edge case where prefix is all 0xFF (no successor exists).
func Test_ScanPrefix_Returns_All_Matching_Entries_When_Prefix_Is_AllFF(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "prefix_ff.cache"),
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 10,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	// Insert keys including ones starting with 0xFF.
	keys := [][]byte{
		{0x00, 0x00},
		{0xFE, 0xFF},
		{0xFF, 0x00},
		{0xFF, 0x01},
		{0xFF, 0xFF},
	}

	for i, key := range keys {
		putErr := w.Put(key, int64(i), nil)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// ScanPrefix for 0xFF should return 3 entries (0xFF00, 0xFF01, 0xFFFF).
	entries, err := c.ScanPrefix([]byte{0xFF}, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("ScanPrefix(0xFF): %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("ScanPrefix(0xFF): expected 3 entries, got %d", len(entries))
	}

	for _, e := range entries {
		if e.Key[0] != 0xFF {
			t.Errorf("ScanPrefix(0xFF): unexpected key %x", e.Key)
		}
	}
}

// Test_ScanMatch_Returns_Correct_Results_When_Using_BitLevel_Prefix_In_OrderedKeys verifies that ScanMatch with
// bit-level prefix uses range optimization in ordered-keys mode.
func Test_ScanMatch_Returns_Correct_Results_When_Using_BitLevel_Prefix_In_OrderedKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "prefix_bits.cache"),
		KeySize:      2,
		IndexSize:    0,
		SlotCapacity: 20,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	// Insert keys that will be partially matched by bit-level prefix.
	// A 4-bit prefix of 0xA0 (1010xxxx) matches 0xA0-0xAF.
	keys := [][]byte{
		{0x90, 0x00}, // 1001xxxx - no match
		{0xA0, 0x00}, // 1010xxxx - match
		{0xA5, 0x00}, // 1010xxxx - match
		{0xAF, 0xFF}, // 1010xxxx - match
		{0xB0, 0x00}, // 1011xxxx - no match
	}

	for i, key := range keys {
		putErr := w.Put(key, int64(i), nil)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// ScanMatch with 4-bit prefix 0xA (1010) should match keys 0xA0-0xAF.
	spec := slotcache.Prefix{
		Offset: 0,
		Bits:   4,
		Bytes:  []byte{0xA0}, // Upper 4 bits: 1010
	}

	entries, err := c.ScanMatch(spec, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("ScanMatch(4-bit 0xA): %v", err)
	}

	if len(entries) != 3 {
		t.Errorf("ScanMatch(4-bit 0xA): expected 3 entries, got %d", len(entries))
	}

	for _, e := range entries {
		// Check upper 4 bits are 1010 (0xA).
		if (e.Key[0] >> 4) != 0x0A {
			t.Errorf("ScanMatch(4-bit 0xA): unexpected key %x (upper 4 bits: %x)", e.Key, e.Key[0]>>4)
		}
	}
}

// Test_ScanMatch_Falls_Back_To_Full_Scan_When_Prefix_Offset_Is_NonZero verifies that ScanMatch falls back
// to full scan when prefix offset is not 0 (even in ordered-keys mode).
func Test_ScanMatch_Falls_Back_To_Full_Scan_When_Prefix_Offset_Is_NonZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "prefix_offset.cache"),
		KeySize:      4,
		IndexSize:    0,
		SlotCapacity: 10,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	w, beginErr := c.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite: %v", beginErr)
	}

	keys := [][]byte{
		{0x00, 0xAA, 0x00, 0x00},
		{0x01, 0xAA, 0x00, 0x00},
		{0x02, 0xBB, 0x00, 0x00},
	}

	for i, key := range keys {
		putErr := w.Put(key, int64(i), nil)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// ScanMatch with prefix at offset 1 should still work (falls back to full scan).
	spec := slotcache.Prefix{
		Offset: 1,
		Bits:   0,
		Bytes:  []byte{0xAA},
	}

	entries, err := c.ScanMatch(spec, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("ScanMatch(offset=1): %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("ScanMatch(offset=1): expected 2 entries, got %d", len(entries))
	}

	for _, e := range entries {
		if e.Key[1] != 0xAA {
			t.Errorf("ScanMatch(offset=1): unexpected key %x", e.Key)
		}
	}
}

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

	w, err := c.Writer()
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

	w, err := c.Writer()
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

	w, err := c.Writer()
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

	w, err := c.Writer()
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

	w, err := c.Writer()
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

	w, err := c.Writer()
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

	w, err := c.Writer()
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
	w2, err := c.Writer()
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

	w, err := c.Writer()
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

// Test_Scan_Skips_Tombstones_When_Reverse_OrderedKeys verifies that reverse scans
// in ordered-keys mode correctly skip tombstones while iterating backward.
// This exercises the doCollectReverse path.
func Test_Scan_Skips_Tombstones_When_Reverse_OrderedKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "reverse_tombstones.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert entries 0-9 in order.
	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}

	for i := range 10 {
		key := []byte{0, 0, 0, byte(i)}

		idx := []byte{byte(i), 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Delete entries 3, 5, 7 (create tombstones in the middle).
	w, err = c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite for delete: %v", err)
	}

	for _, i := range []int{3, 5, 7} {
		key := []byte{0, 0, 0, byte(i)}

		_, delErr := w.Delete(key)
		if delErr != nil {
			t.Fatalf("Delete %d: %v", i, delErr)
		}
	}

	commitErr = w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit delete: %v", commitErr)
	}

	w.Close()

	// Verify forward scan first.
	entries, err := c.Scan(slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("Forward scan: %v", err)
	}

	expectedForward := []int{0, 1, 2, 4, 6, 8, 9}
	if len(entries) != len(expectedForward) {
		t.Fatalf("expected %d entries, got %d", len(expectedForward), len(entries))
	}

	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedForward[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("forward entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}

	// Reverse scan should give same entries in reverse order.
	entries, err = c.Scan(slotcache.ScanOptions{Reverse: true})
	if err != nil {
		t.Fatalf("Reverse scan: %v", err)
	}

	if len(entries) != len(expectedForward) {
		t.Fatalf("expected %d entries, got %d", len(expectedForward), len(entries))
	}

	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedForward[len(expectedForward)-1-i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("reverse entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}

	// Reverse scan with Limit should get last N entries.
	entries, err = c.Scan(slotcache.ScanOptions{Reverse: true, Limit: 3})
	if err != nil {
		t.Fatalf("Reverse scan with Limit: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Expected: 9, 8, 6 (skipping tombstoned 7).
	expectedReverse := []int{9, 8, 6}
	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedReverse[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("reverse limited entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}
}

// Test_ScanRange_Skips_Tombstones_When_Reverse verifies that reverse range scans
// correctly handle tombstones in the range.
func Test_ScanRange_Skips_Tombstones_When_Reverse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "range_reverse_tombstones.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert entries 0-9.
	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}

	for i := range 10 {
		key := []byte{0, 0, 0, byte(i)}

		idx := []byte{byte(i), 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Delete entries 4, 6 within our test range.
	w, err = c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite for delete: %v", err)
	}

	for _, i := range []int{4, 6} {
		key := []byte{0, 0, 0, byte(i)}

		_, delErr := w.Delete(key)
		if delErr != nil {
			t.Fatalf("Delete %d: %v", i, delErr)
		}
	}

	commitErr = w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit delete: %v", commitErr)
	}

	w.Close()

	// Range [3, 8) should include: 3, 5, 7 (4 and 6 are tombstoned).
	start := []byte{0, 0, 0, 3}
	end := []byte{0, 0, 0, 8}

	// Forward range scan.
	entries, err := c.ScanRange(start, end, slotcache.ScanOptions{})
	if err != nil {
		t.Fatalf("Forward range scan: %v", err)
	}

	expectedForward := []int{3, 5, 7}
	if len(entries) != len(expectedForward) {
		t.Fatalf("expected %d entries, got %d", len(expectedForward), len(entries))
	}

	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedForward[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("forward entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}

	// Reverse range scan.
	entries, err = c.ScanRange(start, end, slotcache.ScanOptions{Reverse: true})
	if err != nil {
		t.Fatalf("Reverse range scan: %v", err)
	}

	if len(entries) != len(expectedForward) {
		t.Fatalf("expected %d entries, got %d", len(expectedForward), len(entries))
	}

	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedForward[len(expectedForward)-1-i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("reverse entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}
}

// Test_Scan_Applies_Filter_When_Reverse_OrderedKeys verifies that reverse scans
// in ordered-keys mode correctly apply filters.
func Test_Scan_Applies_Filter_When_Reverse_OrderedKeys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	opts := slotcache.Options{
		Path:         filepath.Join(dir, "reverse_filter.cache"),
		KeySize:      4,
		IndexSize:    4,
		SlotCapacity: 100,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	// Insert 10 entries with alternating markers.
	w, err := c.Writer()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}

	for i := range 10 {
		key := []byte{0, 0, 0, byte(i)}
		marker := byte(i % 2) // 0 for even, 1 for odd

		idx := []byte{marker, 0, 0, 0}

		putErr := w.Put(key, int64(i), idx)
		if putErr != nil {
			t.Fatalf("Put %d: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit: %v", commitErr)
	}

	w.Close()

	// Filter for even entries only.
	evenFilter := func(e slotcache.Entry) bool {
		return e.Index[0] == 0
	}

	// Forward scan with filter: 0, 2, 4, 6, 8.
	entries, err := c.Scan(slotcache.ScanOptions{Filter: evenFilter})
	if err != nil {
		t.Fatalf("Forward scan with filter: %v", err)
	}

	expectedForward := []int{0, 2, 4, 6, 8}
	if len(entries) != len(expectedForward) {
		t.Fatalf("expected %d entries, got %d", len(expectedForward), len(entries))
	}

	// Reverse scan with filter: 8, 6, 4, 2, 0.
	entries, err = c.Scan(slotcache.ScanOptions{Filter: evenFilter, Reverse: true})
	if err != nil {
		t.Fatalf("Reverse scan with filter: %v", err)
	}

	if len(entries) != len(expectedForward) {
		t.Fatalf("expected %d entries, got %d", len(expectedForward), len(entries))
	}

	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedForward[len(expectedForward)-1-i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("reverse entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}

	// Reverse scan with filter and Limit.
	entries, err = c.Scan(slotcache.ScanOptions{Filter: evenFilter, Reverse: true, Limit: 2})
	if err != nil {
		t.Fatalf("Reverse scan with filter and Limit: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Expected: 8, 6.
	expectedReverse := []int{8, 6}
	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedReverse[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("reverse limited entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}

	// Reverse scan with filter, Offset, and Limit.
	entries, err = c.Scan(slotcache.ScanOptions{Filter: evenFilter, Reverse: true, Offset: 1, Limit: 2})
	if err != nil {
		t.Fatalf("Reverse scan with filter, Offset, and Limit: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Reverse order: 8, 6, 4, 2, 0. Skip 1 -> 6, 4.
	expectedReverse = []int{6, 4}
	for i, e := range entries {
		exp := []byte{0, 0, 0, byte(expectedReverse[i])}
		if !bytes.Equal(e.Key, exp) {
			t.Errorf("reverse offset limited entry %d: expected %v, got %v", i, exp, e.Key)
		}
	}
}

// Test_Scan_Returns_Correct_Results_When_Reverse_Edge_Cases verifies reverse scan behavior for edge cases.
func Test_Scan_Returns_Correct_Results_When_Reverse_Edge_Cases(t *testing.T) {
	t.Parallel()

	t.Run("empty cache", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "empty.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		entries, err := c.Scan(slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse scan empty: %v", err)
		}

		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})

	t.Run("single entry", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "single.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		w, err := c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}

		key := []byte{0, 0, 0, 1}

		putErr := w.Put(key, 1, []byte{1, 0, 0, 0})
		if putErr != nil {
			t.Fatalf("Put: %v", putErr)
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		w.Close()

		entries, err := c.Scan(slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse scan single: %v", err)
		}

		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}

		if !bytes.Equal(entries[0].Key, key) {
			t.Errorf("expected key %v, got %v", key, entries[0].Key)
		}
	})

	t.Run("all tombstones", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "all_tombstones.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		// Insert then delete all entries.
		w, err := c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}

		for i := range 5 {
			key := []byte{0, 0, 0, byte(i)}

			putErr := w.Put(key, int64(i), []byte{byte(i), 0, 0, 0})
			if putErr != nil {
				t.Fatalf("Put %d: %v", i, putErr)
			}
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		w.Close()

		w, err = c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite for delete: %v", err)
		}

		for i := range 5 {
			key := []byte{0, 0, 0, byte(i)}

			_, delErr := w.Delete(key)
			if delErr != nil {
				t.Fatalf("Delete %d: %v", i, delErr)
			}
		}

		commitErr = w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit delete: %v", commitErr)
		}

		w.Close()

		entries, err := c.Scan(slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse scan all tombstones: %v", err)
		}

		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})
}

// Test_ScanRange_Returns_Correct_Results_When_Reverse_Edge_Cases verifies reverse range scan edge cases.
func Test_ScanRange_Returns_Correct_Results_When_Reverse_Edge_Cases(t *testing.T) {
	t.Parallel()

	t.Run("range outside data", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "range_outside.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		// Insert entries 5-9.
		w, err := c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}

		for i := 5; i < 10; i++ {
			key := []byte{0, 0, 0, byte(i)}

			putErr := w.Put(key, int64(i), []byte{byte(i), 0, 0, 0})
			if putErr != nil {
				t.Fatalf("Put %d: %v", i, putErr)
			}
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		w.Close()

		// Range [0, 3) - completely before data.
		start := []byte{0, 0, 0, 0}
		end := []byte{0, 0, 0, 3}

		entries, err := c.ScanRange(start, end, slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse range scan: %v", err)
		}

		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}

		// Range [15, 20) - completely after data.
		start = []byte{0, 0, 0, 15}
		end = []byte{0, 0, 0, 20}

		entries, err = c.ScanRange(start, end, slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse range scan after: %v", err)
		}

		if len(entries) != 0 {
			t.Errorf("expected 0 entries, got %d", len(entries))
		}
	})

	t.Run("partial range overlap", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "partial_overlap.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		// Insert entries 5-9.
		w, err := c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}

		for i := 5; i < 10; i++ {
			key := []byte{0, 0, 0, byte(i)}

			putErr := w.Put(key, int64(i), []byte{byte(i), 0, 0, 0})
			if putErr != nil {
				t.Fatalf("Put %d: %v", i, putErr)
			}
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		w.Close()

		// Range [3, 7) - partial overlap, should get 5, 6.
		start := []byte{0, 0, 0, 3}
		end := []byte{0, 0, 0, 7}

		entries, err := c.ScanRange(start, end, slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse range scan: %v", err)
		}

		if len(entries) != 2 {
			t.Fatalf("expected 2 entries, got %d", len(entries))
		}

		// Reverse: 6, 5.
		expected := []int{6, 5}
		for i, e := range entries {
			exp := []byte{0, 0, 0, byte(expected[i])}
			if !bytes.Equal(e.Key, exp) {
				t.Errorf("entry %d: expected %v, got %v", i, exp, e.Key)
			}
		}
	})

	t.Run("unbounded start reverse", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "unbounded_start.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		// Insert entries 0-4.
		w, err := c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}

		for i := range 5 {
			key := []byte{0, 0, 0, byte(i)}

			putErr := w.Put(key, int64(i), []byte{byte(i), 0, 0, 0})
			if putErr != nil {
				t.Fatalf("Put %d: %v", i, putErr)
			}
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		w.Close()

		// Range [nil, 3) - unbounded start.
		end := []byte{0, 0, 0, 3}

		entries, err := c.ScanRange(nil, end, slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse range scan unbounded start: %v", err)
		}

		if len(entries) != 3 {
			t.Fatalf("expected 3 entries, got %d", len(entries))
		}

		// Reverse: 2, 1, 0.
		expected := []int{2, 1, 0}
		for i, e := range entries {
			exp := []byte{0, 0, 0, byte(expected[i])}
			if !bytes.Equal(e.Key, exp) {
				t.Errorf("entry %d: expected %v, got %v", i, exp, e.Key)
			}
		}
	})

	t.Run("unbounded end reverse", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "unbounded_end.cache"),
			KeySize:      4,
			IndexSize:    4,
			SlotCapacity: 100,
			OrderedKeys:  true,
		}

		c, err := slotcache.Open(opts)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer c.Close()

		// Insert entries 0-4.
		w, err := c.Writer()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}

		for i := range 5 {
			key := []byte{0, 0, 0, byte(i)}

			putErr := w.Put(key, int64(i), []byte{byte(i), 0, 0, 0})
			if putErr != nil {
				t.Fatalf("Put %d: %v", i, putErr)
			}
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit: %v", commitErr)
		}

		w.Close()

		// Range [2, nil) - unbounded end.
		start := []byte{0, 0, 0, 2}

		entries, err := c.ScanRange(start, nil, slotcache.ScanOptions{Reverse: true})
		if err != nil {
			t.Fatalf("Reverse range scan unbounded end: %v", err)
		}

		if len(entries) != 3 {
			t.Fatalf("expected 3 entries, got %d", len(entries))
		}

		// Reverse: 4, 3, 2.
		expected := []int{4, 3, 2}
		for i, e := range entries {
			exp := []byte{0, 0, 0, byte(expected[i])}
			if !bytes.Equal(e.Key, exp) {
				t.Errorf("entry %d: expected %v, got %v", i, exp, e.Key)
			}
		}
	})
}

func Test_Scan_Returns_ErrInvalidInput_When_Offset_Exceeds_Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_offset_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	_, scanErr := c.Scan(slotcache.ScanOptions{Offset: 100_000_001, Limit: 0})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("Scan(offset cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

func Test_Scan_Returns_ErrInvalidInput_When_Limit_Exceeds_Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_limit_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	_, scanErr := c.Scan(slotcache.ScanOptions{Offset: 0, Limit: 100_000_001})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("Scan(limit cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

func Test_ScanMatch_Returns_ErrInvalidInput_When_PrefixBits_Exceed_KeyCapacity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanmatch_prefixbits_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	// Bits is intentionally huge. The implementation must reject it as invalid
	// without overflowing internal arithmetic.
	_, scanErr := c.ScanMatch(slotcache.Prefix{Offset: 0, Bits: 1 << 30, Bytes: nil}, slotcache.ScanOptions{Offset: 0, Limit: 0})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("ScanMatch(prefix bits cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

func Test_ScanRange_Returns_ErrInvalidInput_When_Offset_Exceeds_Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_offset_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 8,
		OrderedKeys:  true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	_, scanErr := c.ScanRange(nil, nil, slotcache.ScanOptions{Offset: 100_000_001, Limit: 0})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("ScanRange(offset cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

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

	w, beginErr := cache.Writer()
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

	w, beginErr := cache.Writer()
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

	w, beginErr := cache.Writer()
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
