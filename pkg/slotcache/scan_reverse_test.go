package slotcache_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

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
	w, err := c.BeginWrite()
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
	w, err = c.BeginWrite()
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
	w, err := c.BeginWrite()
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
	w, err = c.BeginWrite()
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
	w, err := c.BeginWrite()
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

		w, err := c.BeginWrite()
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
		w, err := c.BeginWrite()
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

		w, err = c.BeginWrite()
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
		w, err := c.BeginWrite()
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
		w, err := c.BeginWrite()
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
		w, err := c.BeginWrite()
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
		w, err := c.BeginWrite()
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
