// Filter seed guard test.
//
// This test verifies that the fuzz corpus seeds for filter coverage
// still emit at least one scan operation with Filter != nil.
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
// Fix by updating the seed bytes in internal/testutil/behavior_seeds.go
// to match the current decoder behavior.

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_BehaviorFuzz_Emits_FilteredScan_When_Using_Curated_FilterSeeds(t *testing.T) {
	t.Parallel()

	// Use the centralized filter seeds from testutil.
	seeds := testutil.FilterSeeds()

	for _, tc := range seeds {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()

			opts := slotcache.Options{
				Path:         filepath.Join(tmpDir, tc.Name+".slc"),
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			h := testutil.NewHarness(t, opts)

			defer func() { _ = h.Real.Cache.Close() }()

			decoder := testutil.NewFuzzDecoder(tc.Data, opts)

			var previouslySeenKeys [][]byte

			const maxOps = testutil.DefaultMaxFuzzOperations

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
				t.Fatalf("seed %q emitted no scan op with Filter != nil; update seed bytes in testutil/behavior_seeds.go", tc.Name)
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
