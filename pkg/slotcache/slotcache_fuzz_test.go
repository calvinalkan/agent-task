// Fuzz tests comparing slotcache against an in-memory reference model.
// Catches logic bugs in API behavior (Get, Put, Delete, Scan, transactions).
//
// Failures mean: the API returned wrong results or wrong errors.

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// Uses fixed options (KeySize=8, IndexSize=4, SlotCapacity=64) for deep
// coverage on a single configuration.
func FuzzSlotcache_Matches_Model_When_Random_Ops_Applied(f *testing.F) {
	// Seeds: raw bytes for operations.
	f.Add([]byte{0x00, 0x01, 0x02})
	f.Add([]byte{0xFF, 0xFE, 0xFD})
	f.Add([]byte("slotcache-ops"))
	f.Add(make([]byte, 64))

	// Curated seeds from testutil.
	for _, seed := range testutil.AllSeeds() {
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

		cfg := testutil.CurratedSeedOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

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

// scaleMaxOpsForOptions reduces MaxOps for heavier option profiles to keep
// fuzz iterations fast while still exercising the derived configurations.
func scaleMaxOpsForOptions(opts slotcache.Options) int {
	baseOps := testutil.DefaultMaxFuzzOperations

	combined := opts.KeySize + opts.IndexSize
	if combined > 32 {
		baseOps = baseOps * 60 / 100
	} else if combined > 20 {
		baseOps = baseOps * 80 / 100
	}

	if opts.SlotCapacity > 100 {
		baseOps = baseOps * 80 / 100
	}

	if baseOps < 50 {
		baseOps = 50
	}

	return baseOps
}

// Same as above but with OrderedKeys enabled.
func FuzzSlotcache_Matches_Model_When_Random_Ops_Applied_With_OrderedKeys(f *testing.F) {
	// Seeds: raw bytes for operations.
	f.Add([]byte{0x00, 0x01, 0x02})
	f.Add([]byte{0xFF, 0xFE, 0xFD})
	f.Add([]byte("slotcache-ordered-ops"))
	f.Add(make([]byte, 64))

	// Curated seeds from testutil.
	for _, seed := range testutil.OrderedBehaviorSeeds() {
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

		cfg := testutil.CurratedSeedOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

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

// Derives options from fuzz bytes to exercise alignment/padding edge cases,
// IndexSize==0, tiny capacities, and ordered/unordered modes.
func FuzzSlotcache_Matches_Model_When_Random_Ops_Applied_With_Derived_Options(f *testing.F) {
	// Seeds: encoded options + bytes for operations.
	commonOpts := testutil.OptionsToSeed(slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64})
	f.Add(append(commonOpts, 0x01, 0x02, 0x03, 0x04, 0x05))

	tinyOrdered := testutil.OptionsToSeed(slotcache.Options{KeySize: 1, IndexSize: 0, SlotCapacity: 1, OrderedKeys: true})
	f.Add(append(tinyOrdered, 0xFF, 0xFE, 0xFD, 0xFC))

	maxSizes := testutil.OptionsToSeed(slotcache.Options{KeySize: 32, IndexSize: 32, SlotCapacity: 1})
	f.Add(append(maxSizes, 0xAA, 0xBB, 0xCC, 0xDD))

	minimalKey := testutil.OptionsToSeed(slotcache.Options{KeySize: 1, IndexSize: 0, SlotCapacity: 8})
	f.Add(append(minimalKey, 0x10, 0x20, 0x30, 0x40))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_opts.slc")

		options, rest := testutil.OptionsFromSeed(fuzzBytes, cacheFilePath)

		cfg := testutil.CurratedSeedOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(rest, options, &cfg)

		maxOps := scaleMaxOpsForOptions(options)

		runCfg := testutil.BehaviorRunConfig{
			MaxOps:               maxOps,
			LightCompareEveryN:   10,
			HeavyCompareEveryN:   0,
			CompareOnCommit:      true,
			CompareOnCloseReopen: true,
		}

		testutil.RunBehavior(t, options, opGen, runCfg)
	})
}

// Uses large KeySize (512) and IndexSize (16KB) to stress record-layout
// arithmetic, large key/index copy paths, and scan/filtering behavior.
func FuzzSlotcache_Matches_Model_When_Random_Ops_Applied_With_Large_Records(f *testing.F) {
	// Seeds: raw bytes for operations (larger to allow multiple puts with 16KB index).
	f.Add([]byte{0x00, 0x01, 0x02, 0x03})
	f.Add([]byte{0xFF, 0xFE, 0xFD, 0xFC})
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_nearcap.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      512,
			IndexSize:    16 * 1024,
			SlotCapacity: 64,
		}

		cfg := testutil.CurratedSeedOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

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

// Same as above but with OrderedKeys enabled.
func FuzzSlotcache_Matches_Model_When_Random_Ops_Applied_With_Large_Records_And_OrderedKeys(f *testing.F) {
	// Seeds: raw bytes for operations (larger to allow multiple puts with 16KB index).
	f.Add([]byte{0x10, 0x11, 0x12, 0x13})
	f.Add([]byte{0xAA, 0xBB, 0xCC, 0xDD})
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_nearcap_ordered.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      512,
			IndexSize:    16 * 1024,
			SlotCapacity: 64,
			OrderedKeys:  true,
		}

		cfg := testutil.CurratedSeedOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

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
