// Filter seed guard test.
//
// This test verifies that the fuzz corpus seeds for filter coverage
// (seedBehaviorFilteredScans, seedBehaviorFilterPagination) still emit
// at least one scan operation with Filter != nil.
//
// Purpose:
//   - Acts as a "tripwire" if the fuzz decoder's byte consumption changes
//   - Ensures filter coverage is maintained in the fuzz corpus
//   - Fails fast with a clear message if seeds need updating
//
// If this test fails, it means:
//  1. The seed bytes no longer trigger the filter code path, OR
//  2. The decoder's nextFilterSpec or scan op byte consumption changed
//
// Fix by updating the seed bytes in behavior_filter_seeddata_test.go to
// match the current decoder behavior.

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_BehaviorFuzz_FilterSeeds_Emit_AtLeast_One_Filtered_Scan(t *testing.T) {
	t.Parallel()

	seeds := []struct {
		name string
		data []byte
	}{
		{name: "FilteredScans", data: seedBehaviorFilteredScans},
		{name: "FilterPagination", data: seedBehaviorFilterPagination},
	}

	for _, tc := range seeds {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()

			opts := slotcache.Options{
				Path:         filepath.Join(tmpDir, tc.name+".slc"),
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			h := testutil.NewHarness(t, opts)

			defer func() { _ = h.Real.Cache.Close() }()

			decoder := testutil.NewFuzzDecoder(tc.data, opts)

			var previouslySeenKeys [][]byte

			const maxOps = maxFuzzOperations

			sawFilteredScan := false

			for i := 0; i < maxOps && decoder.HasMore(); i++ {
				op := decoder.NextOp(h, previouslySeenKeys)

				if opHasNonNilFilter(op) {
					sawFilteredScan = true
				}

				mRes := testutil.ApplyModel(h, op)
				rRes := testutil.ApplyReal(h, op)

				testutil.RememberPutKey(op, mRes, opts.KeySize, &previouslySeenKeys)

				// Keeps harness state aligned and gives better failure output.
				testutil.AssertOpMatch(t, op, mRes, rRes)
			}

			if !sawFilteredScan {
				t.Fatalf("seed %q emitted no scan op with Filter != nil; update seed bytes or decoder", tc.name)
			}
		})
	}
}

func opHasNonNilFilter(op testutil.Operation) bool {
	switch v := op.(type) {
	case testutil.OpScan:
		return v.Filter != nil
	case testutil.OpScanPrefix:
		return v.Filter != nil
	case testutil.OpScanMatch:
		return v.Filter != nil
	case testutil.OpScanRange:
		return v.Filter != nil
	default:
		return false
	}
}
