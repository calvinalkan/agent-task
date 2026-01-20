package slotcache_test

import (
	"bytes"
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
	w, beginErr := c.BeginWrite()
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
	w, beginErr := c.BeginWrite()
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
	w, beginErr := c.BeginWrite()
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
	w, beginErr := c.BeginWrite()
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

	w, beginErr := c.BeginWrite()
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

	w, beginErr := c.BeginWrite()
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

	w, beginErr := c.BeginWrite()
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
