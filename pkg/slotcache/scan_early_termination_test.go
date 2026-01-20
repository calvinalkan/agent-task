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
