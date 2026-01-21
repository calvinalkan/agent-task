// Behavioral correctness: fuzz testing with derived options
//
// Oracle: in-memory behavioral model (internal/testutil/model)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// This fuzz target differs from FuzzBehavior_ModelVsReal by deriving options
// from fuzz bytes, exercising alignment/padding edge cases, IndexSize==0,
// tiny capacities, and both ordered/unordered modes.
//
// Uses testutil.RunBehavior + OpGenerator for shared operation generation
// and comparison logic with other behavior tests.
//
// Failures here mean: "the API returned wrong results or wrong errors"

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzBehavior_ModelVsReal_FuzzOptions is the behavior-level differential fuzzer
// with fuzz-derived Options to exercise alignment/padding, IndexSize==0,
// tiny capacities, and ordered/unordered mode.
//
// Separate corpus from FuzzBehavior_ModelVsReal since the byte layout differs:
// first bytes derive options, remaining bytes drive operations.
func FuzzBehavior_ModelVsReal_FuzzOptions(f *testing.F) {
	// Seeds that exercise various option derivations.
	f.Add([]byte{})
	f.Add([]byte{7, 4, 0, 15, 0, 0x01, 0x02, 0x03}) // common config + some ops
	f.Add([]byte{0, 0, 0, 0, 1, 0xFF, 0xFF})        // smallest config, ordered
	f.Add([]byte{31, 32, 0, 0, 0})                  // max key/index size, tiny capacity
	f.Add([]byte{0, 0, 4, 7, 0})                    // minimal key, interesting capacity selector
	f.Add([]byte("options+ops"))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_opts.slc")

		options, rest := testutil.DeriveFuzzOptions(fuzzBytes, cacheFilePath)

		// Use DefaultOpGenConfig which handles ordered mode automatically.
		cfg := testutil.DefaultOpGenConfig()
		opGen := testutil.NewOpGenerator(rest, options, &cfg)

		// Scale MaxOps based on option heaviness.
		// Larger key/index sizes and capacities = slower operations = fewer ops.
		maxOps := scaleMaxOpsForOptions(options)

		// Fuzz-optimized run config:
		// - Heavy comparisons on state-changing events (commit/close/reopen)
		// - Light comparisons every 10 ops for fast throughput
		runCfg := testutil.BehaviorRunConfig{
			MaxOps:               maxOps,
			LightCompareEveryN:   10,
			HeavyCompareEveryN:   0, // Disabled (use event-based instead)
			CompareOnCommit:      true,
			CompareOnCloseReopen: true,
		}

		testutil.RunBehavior(t, options, opGen, runCfg)
	})
}

// scaleMaxOpsForOptions reduces MaxOps for heavier option profiles to keep
// fuzz iterations fast while still exercising the derived configurations.
//
// The scaling factors are based on empirical observation:
// - Large key/index sizes increase per-operation memory and comparison time
// - Large slot capacities increase scan and state comparison time
func scaleMaxOpsForOptions(opts slotcache.Options) int {
	baseOps := testutil.DefaultMaxFuzzOperations // 200

	// Scale down for large key+index sizes (affects copy/comparison costs).
	// KeySize range: 1-32, IndexSize range: 0-32
	// Combined max: 64, baseline: ~12 (8+4 from default profile)
	combined := opts.KeySize + opts.IndexSize
	if combined > 32 {
		// Heavy profile: reduce to 60% of base ops
		baseOps = baseOps * 60 / 100
	} else if combined > 20 {
		// Medium-heavy profile: reduce to 80% of base ops
		baseOps = baseOps * 80 / 100
	}

	// Scale down for large slot capacities (affects scan costs).
	// SlotCapacity range: 1-128, baseline: 64 from default profile
	if opts.SlotCapacity > 100 {
		// Large capacity: reduce by additional 20%
		baseOps = baseOps * 80 / 100
	}

	// Ensure we always run at least 50 ops for meaningful coverage.
	if baseOps < 50 {
		baseOps = 50
	}

	return baseOps
}
