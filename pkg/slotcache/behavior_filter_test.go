package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// Test_Filter_Returns_Correct_Subset_When_Applied_To_Scan tests each filter kind with known data.
func Test_Filter_Returns_Correct_Subset_When_Applied_To_Scan(t *testing.T) {
	t.Parallel()

	newPopulatedHarness := func(t *testing.T) *testutil.Harness {
		t.Helper()

		dir := t.TempDir()
		opts := slotcache.Options{
			Path:         filepath.Join(dir, "filter.cache"),
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 100,
		}

		h := testutil.NewHarness(t, opts)
		t.Cleanup(func() { _ = h.Real.Cache.Close() })

		// Keys: AA prefix vs BB prefix.
		k1 := []byte{0xAA, 0, 0, 0, 0, 0, 0, 1}
		k2 := []byte{0xAA, 0, 0, 0, 0, 0, 0, 2}
		k3 := []byte{0xBB, 0, 0, 0, 0, 0, 0, 3}
		k4 := []byte{0xBB, 0, 0, 0, 0, 0, 0, 4}

		// Index bytes: 0x10 vs 0x20 in first byte.
		i1 := []byte{0x10, 0x00, 0x00, 0x00}
		i2 := []byte{0x10, 0x01, 0x00, 0x00}
		i3 := []byte{0x20, 0x00, 0x00, 0x00}
		i4 := []byte{0x20, 0x01, 0x00, 0x00}

		// Insert data via ops.
		ops := []testutil.Operation{
			testutil.OpBeginWrite{},
			testutil.OpPut{Key: k1, Revision: 1, Index: i1},
			testutil.OpPut{Key: k2, Revision: 2, Index: i2},
			testutil.OpPut{Key: k3, Revision: 3, Index: i3},
			testutil.OpPut{Key: k4, Revision: 4, Index: i4},
			testutil.OpCommit{},
		}

		for _, op := range ops {
			mRes := testutil.ApplyModel(h, op)
			rRes := testutil.ApplyReal(h, op)
			testutil.AssertOpMatch(t, op, mRes, rRes)
		}

		// Verify initial state.
		testutil.CompareState(t, h)

		return h
	}

	// Test FilterAll - should return all 4 entries.
	t.Run("FilterAll", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		spec := testutil.FilterSpec{Kind: testutil.FilterAll}
		op := testutil.OpScan{Filter: &spec, Options: slotcache.ScanOptions{}}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 4 {
			t.Fatalf("FilterAll: expected 4 entries, got %d", len(scanRes.Entries))
		}
	})

	// Test FilterNone - should return 0 entries.
	t.Run("FilterNone", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		spec := testutil.FilterSpec{Kind: testutil.FilterNone}
		op := testutil.OpScan{Filter: &spec, Options: slotcache.ScanOptions{}}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 0 {
			t.Fatalf("FilterNone: expected 0 entries, got %d", len(scanRes.Entries))
		}
	})

	// Test RevisionMask(mask=1, want=0) - even revisions (2, 4) => k2, k4.
	t.Run("RevisionMask_Even", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		spec := testutil.FilterSpec{Kind: testutil.FilterRevisionMask, Mask: 1, Want: 0}
		op := testutil.OpScan{Filter: &spec, Options: slotcache.ScanOptions{}}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 2 {
			t.Fatalf("RevisionMask(even): expected 2 entries, got %d", len(scanRes.Entries))
		}

		// Check revisions are even.
		for _, e := range scanRes.Entries {
			if e.Revision%2 != 0 {
				t.Fatalf("RevisionMask(even): got odd revision %d", e.Revision)
			}
		}
	})

	// Test IndexByteEq(offset=0, byte=0x10) => k1, k2.
	t.Run("IndexByteEq", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		spec := testutil.FilterSpec{Kind: testutil.FilterIndexByteEq, Offset: 0, Byte: 0x10}
		op := testutil.OpScan{Filter: &spec, Options: slotcache.ScanOptions{}}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 2 {
			t.Fatalf("IndexByteEq: expected 2 entries, got %d", len(scanRes.Entries))
		}

		// Check index[0] == 0x10.
		for _, e := range scanRes.Entries {
			if e.Index[0] != 0x10 {
				t.Fatalf("IndexByteEq: expected index[0]=0x10, got 0x%02X", e.Index[0])
			}
		}
	})

	// Test KeyPrefixEq(0xAA) => k1, k2.
	t.Run("KeyPrefixEq", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		spec := testutil.FilterSpec{Kind: testutil.FilterKeyPrefixEq, Prefix: []byte{0xAA}}
		op := testutil.OpScan{Filter: &spec, Options: slotcache.ScanOptions{}}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 2 {
			t.Fatalf("KeyPrefixEq: expected 2 entries, got %d", len(scanRes.Entries))
		}

		// Check key[0] == 0xAA.
		for _, e := range scanRes.Entries {
			if e.Key[0] != 0xAA {
				t.Fatalf("KeyPrefixEq: expected key[0]=0xAA, got 0x%02X", e.Key[0])
			}
		}
	})

	// Test pagination after filter: IndexByteEq with Offset=1, Limit=1 => only k2.
	t.Run("FilterWithPagination", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		spec := testutil.FilterSpec{Kind: testutil.FilterIndexByteEq, Offset: 0, Byte: 0x10}
		op := testutil.OpScan{
			Filter:  &spec,
			Options: slotcache.ScanOptions{Offset: 1, Limit: 1},
		}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 1 {
			t.Fatalf("FilterWithPagination: expected 1 entry, got %d", len(scanRes.Entries))
		}

		// Should be k2 (second entry matching filter).
		if scanRes.Entries[0].Revision != 2 {
			t.Fatalf("FilterWithPagination: expected revision=2, got %d", scanRes.Entries[0].Revision)
		}
	})

	// Test filter with ScanPrefix.
	t.Run("ScanPrefixWithFilter", func(t *testing.T) {
		t.Parallel()
		h := newPopulatedHarness(t)

		// ScanPrefix(0xAA) with RevisionMask(mask=1, want=0) => only k2 (even revision in AA prefix).
		spec := testutil.FilterSpec{Kind: testutil.FilterRevisionMask, Mask: 1, Want: 0}
		op := testutil.OpScanPrefix{
			Prefix:  []byte{0xAA},
			Filter:  &spec,
			Options: slotcache.ScanOptions{},
		}
		mRes := testutil.ApplyModel(h, op)
		rRes := testutil.ApplyReal(h, op)
		testutil.AssertOpMatch(t, op, mRes, rRes)

		scanRes, ok := mRes.(testutil.ResScan)
		if !ok {
			t.Fatal("expected ResScan result")
		}

		if len(scanRes.Entries) != 1 {
			t.Fatalf("ScanPrefixWithFilter: expected 1 entry, got %d", len(scanRes.Entries))
		}

		if scanRes.Entries[0].Revision != 2 {
			t.Fatalf("ScanPrefixWithFilter: expected revision=2, got %d", scanRes.Entries[0].Revision)
		}
	})
}
