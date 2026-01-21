// Behavioral correctness: fuzz testing
//
// Oracle: in-memory behavioral model (internal/testutil/model)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// These tests verify that the real implementation's observable API behavior
// matches the simple in-memory model. They catch logic bugs in Get, Put,
// Delete, Scan, and transaction handling - but NOT file format issues.
//
// Uses testutil.RunBehavior + OpGenerator for shared operation generation
// and comparison logic with behavior_deterministic_seed_test.go.
//
// Failures here mean: "the API returned wrong results or wrong errors"

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzBehavior_ModelVsReal is a coverage-guided fuzz test for public behavior.
//
// Uses fixed options (KeySize=8, IndexSize=4, SlotCapacity=64) for deep
// coverage on a single configuration. See FuzzBehavior_ModelVsReal_Options
// for derived-options fuzzing.
func FuzzBehavior_ModelVsReal(f *testing.F) {
	// A small seed corpus helps the fuzzer reach deeper states quickly.
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	f.Add([]byte("slotcache"))
	f.Add(make([]byte, 64))

	// Add curated behavior seeds from testutil.
	// These are carefully constructed byte sequences that exercise specific
	// code paths. See testutil/behavior_seeds.go for documentation.
	for _, seed := range testutil.AllBehaviorSeeds() {
		f.Add(seed.Data)
	}

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 64,
		}

		// Use CanonicalOpGenConfig with BehaviorOpSet to ensure curated seeds
		// (from AllBehaviorSeeds) remain meaningful. This config enables phased
		// generation and the full behavior operation set including UserHeader ops.
		cfg := testutil.CanonicalOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

		// Fuzz-optimized run config:
		// - Heavy comparisons only on state-changing events (commit/close/reopen)
		// - Light comparisons every 10 ops to maintain coverage without slowing throughput
		runCfg := testutil.BehaviorRunConfig{
			MaxOps:               testutil.DefaultMaxFuzzOperations,
			LightCompareEveryN:   10, // Light check every 10 ops for throughput
			HeavyCompareEveryN:   0,  // Disabled (use event-based instead)
			CompareOnCommit:      true,
			CompareOnCloseReopen: true,
		}

		testutil.RunBehavior(t, options, opGen, runCfg)
	})
}

// FuzzBehavior_ModelVsReal_OrderedKeys is the same as FuzzBehavior_ModelVsReal,
// but runs with OrderedKeys enabled to exercise ordered-mode semantics.
func FuzzBehavior_ModelVsReal_OrderedKeys(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	f.Add([]byte("slotcache"))
	f.Add(make([]byte, 64))

	// Add curated behavior seeds from testutil.
	for _, seed := range testutil.AllBehaviorSeeds() {
		f.Add(seed.Data)
	}

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_ordered.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			SlotCapacity: 64,
			OrderedKeys:  true,
		}

		// Use CanonicalOpGenConfig with BehaviorOpSet to ensure curated seeds
		// remain meaningful. The config handles ordered mode automatically.
		cfg := testutil.CanonicalOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

		// Fuzz-optimized run config (same as unordered).
		runCfg := testutil.BehaviorRunConfig{
			MaxOps:               testutil.DefaultMaxFuzzOperations,
			LightCompareEveryN:   10,
			HeavyCompareEveryN:   0,
			CompareOnCommit:      true,
			CompareOnCloseReopen: true,
		}

		testutil.RunBehavior(t, options, opGen, runCfg)
	})
}
