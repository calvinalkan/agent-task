// Filter seed guard test.
//
// This test verifies that the fuzz corpus seeds for filter coverage
// still emit at least one scan operation with Filter != nil.
//
// Purpose:
//   - Acts as a "tripwire" if the OpGenerator's byte consumption changes
//   - Ensures filter coverage is maintained in the fuzz corpus
//   - Fails fast with a clear message if seeds need updating
//
// If this test fails, it means:
//  1. The seed bytes no longer trigger the filter code path, OR
//  2. The decoder's nextFilterSpec or scan op byte consumption changed
//
// Fix by updating the seed bytes in internal/testutil/behavior_seeds.go
// to match the current decoder behavior.
//
// WHY THIS LIVES IN pkg/slotcache (not internal/testutil):
// Guard tests must run with `go test ./pkg/slotcache` to catch regressions
// during normal development. Tests in internal/testutil/ only run when
// explicitly targeting that package, which isn't part of typical workflows.
// The helpers live in testutil; the guards that USE them live here.

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_BehaviorFuzz_Emits_FilteredScan_When_Using_Curated_FilterSeeds(t *testing.T) {
	t.Parallel()

	seeds := testutil.FilterSeeds()

	for _, tc := range seeds {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			opts := testutil.DefaultGuardOptions(filepath.Join(tmpDir, tc.Name+".slc"))

			testutil.AssertSeedEmitsFilteredScan(t, tc.Data, opts, testutil.DefaultMaxFuzzOperations)
		})
	}
}
