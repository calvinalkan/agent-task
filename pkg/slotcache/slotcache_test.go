// Deterministic tests comparing slotcache against an in-memory reference model.
// Uses seeded PRNG for reproducible operation sequences across multiple config profiles.
//
// Failures mean: the API returned wrong results or wrong errors.

package slotcache_test

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// testProfile defines a cache configuration for deterministic testing.
type testProfile struct {
	name string
	opts slotcache.Options
}

// Profiles ordered from most constrained to least constrained.
var unorderedProfiles = []testProfile{
	{"KeySize16_IndexSize0_Capacity1", slotcache.Options{KeySize: 16, IndexSize: 0, SlotCapacity: 1}},
	{"KeySize1_IndexSize0_Capacity2", slotcache.Options{KeySize: 1, IndexSize: 0, SlotCapacity: 2}},
	{"KeySize7_IndexSize0_Capacity4", slotcache.Options{KeySize: 7, IndexSize: 0, SlotCapacity: 4}},
	{"KeySize9_IndexSize3_Capacity8", slotcache.Options{KeySize: 9, IndexSize: 3, SlotCapacity: 8}},
}

var orderedProfile = testProfile{
	"KeySize8_IndexSize4_Capacity8_Ordered",
	slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 8, OrderedKeys: true},
}

// Runs deterministic random operations across multiple config profiles.
func Test_Slotcache_Matches_Model_When_Seeded_Random_Ops_Applied(t *testing.T) {
	t.Parallel()

	seedsPerProfile := 10
	if testing.Short() {
		seedsPerProfile = 2
	}

	bytesPerSeed := 8192

	for _, profile := range unorderedProfiles {
		for seedIndex := range seedsPerProfile {
			seed := uint64(seedIndex + 1)
			testName := fmt.Sprintf("%s/seed=%d", profile.name, seed)

			t.Run(testName, func(t *testing.T) {
				t.Parallel()

				opts := profile.opts
				opts.Path = filepath.Join(t.TempDir(), "test.slc")

				rng := rand.New(rand.NewPCG(seed, seed))
				fuzzBytes := make([]byte, bytesPerSeed)
				fillRandom(rng, fuzzBytes)

				cfg := testutil.DeepStateOpGenConfig()
				cfg.AllowedOps = testutil.BehaviorOpSet
				opGen := testutil.NewOpGenerator(fuzzBytes, opts, &cfg)

				runCfg := testutil.BehaviorRunConfig{
					MaxOps:               testutil.DefaultMaxFuzzOperations,
					LightCompareEveryN:   5,
					HeavyCompareEveryN:   0,
					CompareOnCommit:      true,
					CompareOnCloseReopen: true,
				}

				testutil.RunBehavior(t, opts, opGen, runCfg)
			})
		}
	}
}

// Same as above but with OrderedKeys enabled.
func Test_Slotcache_Matches_Model_When_Seeded_Random_Ops_Applied_With_OrderedKeys(t *testing.T) {
	t.Parallel()

	seedsCount := 25
	if testing.Short() {
		seedsCount = 5
	}

	bytesPerSeed := 8192

	for seedIndex := range seedsCount {
		seed := uint64(10_000 + seedIndex + 1)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			opts := orderedProfile.opts
			opts.Path = filepath.Join(t.TempDir(), "test_ordered.slc")

			rng := rand.New(rand.NewPCG(seed, seed))
			fuzzBytes := make([]byte, bytesPerSeed)
			fillRandom(rng, fuzzBytes)

			cfg := testutil.DeepStateOpGenConfig()
			cfg.AllowedOps = testutil.BehaviorOpSet
			opGen := testutil.NewOpGenerator(fuzzBytes, opts, &cfg)

			runCfg := testutil.BehaviorRunConfig{
				MaxOps:               testutil.DefaultMaxFuzzOperations,
				LightCompareEveryN:   5,
				HeavyCompareEveryN:   0,
				CompareOnCommit:      true,
				CompareOnCloseReopen: true,
			}

			testutil.RunBehavior(t, opts, opGen, runCfg)
		})
	}
}

// Runs curated seeds that exercise specific code paths.
func Test_Slotcache_Matches_Model_When_Curated_Seed_Applied(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.AllSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.SeedOptions
			opts.Path = filepath.Join(t.TempDir(), "test.slc")

			cfg := testutil.CurratedSeedOpGenConfig()
			cfg.AllowedOps = testutil.BehaviorOpSet
			opGen := testutil.NewOpGenerator(seed.Data, opts, &cfg)

			runCfg := testutil.BehaviorRunConfig{
				MaxOps:               200,
				LightCompareEveryN:   5,
				CompareOnCommit:      true,
				CompareOnCloseReopen: true,
			}

			testutil.RunBehavior(t, opts, opGen, runCfg)
		})
	}
}

// Same as above but with OrderedKeys enabled.
func Test_Slotcache_Matches_Model_When_Curated_Seed_Applied_With_OrderedKeys(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.OrderedBehaviorSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.SeedOptions
			opts.OrderedKeys = true
			opts.Path = filepath.Join(t.TempDir(), "test.slc")

			cfg := testutil.CurratedSeedOpGenConfig()
			cfg.AllowedOps = testutil.BehaviorOpSet
			opGen := testutil.NewOpGenerator(seed.Data, opts, &cfg)

			runCfg := testutil.BehaviorRunConfig{
				MaxOps:               200,
				LightCompareEveryN:   5,
				CompareOnCommit:      true,
				CompareOnCloseReopen: true,
			}

			testutil.RunBehavior(t, opts, opGen, runCfg)
		})
	}
}
