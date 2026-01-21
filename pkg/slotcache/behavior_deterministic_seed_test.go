// Behavioral correctness: deterministic seeded testing
//
// Oracle: in-memory behavioral model (internal/testutil/model)
// Technique: deterministic pseudo-random sequences (seeded PRNG)
//
// Uses testutil.RunBehavior with option profiles to exercise different
// cache configurations. Each (profile, seed) combination generates a
// deterministic operation sequence, making failures easy to reproduce.
//
// The test uses DeepStateOpGenConfig for deeper state exploration:
// - Lower invalid input rates (5%) to reach meaningful states
// - Longer writer sessions (commit rate 10%) for more puts per session
// - Higher delete rate (20%) for tombstone stress
// - More eager BeginWrite (30%) to start writing sooner
//
// BehaviorOpSet is used to enable UserHeader operations (SetUserHeaderFlags,
// SetUserHeaderData, UserHeader) which are excluded from the default CoreOpSet.
//
// Failures here mean: "the API returned wrong results or wrong errors"

package slotcache_test

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_Slotcache_Matches_Model_When_Random_Operations_Applied(t *testing.T) {
	t.Parallel()

	profiles := testutil.OptionProfiles()

	// Seeds per profile. Total subtests = len(profiles) * seedsPerProfile.
	seedsPerProfile := 10
	if testing.Short() {
		seedsPerProfile = 2
	}

	// Enough bytes for ~200 operations with the OpGenerator.
	bytesPerSeed := 8192

	for _, profile := range profiles {
		// Skip ordered profile - tested separately with different seed space.
		if profile.Options.OrderedKeys {
			continue
		}

		for seedIndex := range seedsPerProfile {
			seed := uint64(seedIndex + 1)
			testName := fmt.Sprintf("%s/seed=%d", profile.Name, seed)

			t.Run(testName, func(t *testing.T) {
				t.Parallel()

				temporaryDirectory := t.TempDir()
				cachePath := filepath.Join(temporaryDirectory, "test.slc")
				opts := profile.WithPath(cachePath)

				randomNumberGenerator := rand.New(rand.NewPCG(seed, seed))
				fuzzBytes := make([]byte, bytesPerSeed)
				fillRandom(randomNumberGenerator, fuzzBytes)

				cfg := testutil.DeepStateOpGenConfig()
				cfg.AllowedOps = testutil.BehaviorOpSet
				opGen := testutil.NewOpGenerator(fuzzBytes, opts, &cfg)

				runCfg := testutil.BehaviorRunConfig{
					MaxOps:               testutil.DefaultMaxFuzzOperations,
					LightCompareEveryN:   5, // Light check every 5 ops
					HeavyCompareEveryN:   0, // Disabled (use event-based instead)
					CompareOnCommit:      true,
					CompareOnCloseReopen: true,
				}

				testutil.RunBehavior(t, opts, opGen, runCfg)
			})
		}
	}
}

func Test_Slotcache_Matches_Model_When_Random_Operations_Applied_In_OrderedKeys_Mode(t *testing.T) {
	t.Parallel()

	// Find the ordered profile from testutil.
	var orderedProfile *testutil.OptionsProfile

	for _, p := range testutil.OptionProfiles() {
		if p.Options.OrderedKeys {
			orderedProfile = &p

			break
		}
	}

	if orderedProfile == nil {
		t.Fatal("no ordered profile found in testutil.OptionProfiles()")
	}

	// Ordered mode is stricter about inserts, so we run fewer seeds but
	// use a larger capacity (profile has SlotCapacity=8).
	seedsCount := 25
	if testing.Short() {
		seedsCount = 5
	}

	bytesPerSeed := 8192

	for seedIndex := range seedsCount {
		// Use a different seed space (10000+) to avoid overlap with unordered tests.
		seed := uint64(10_000 + seedIndex + 1)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			temporaryDirectory := t.TempDir()
			cachePath := filepath.Join(temporaryDirectory, "test_ordered.slc")
			opts := orderedProfile.WithPath(cachePath)

			randomNumberGenerator := rand.New(rand.NewPCG(seed, seed))
			fuzzBytes := make([]byte, bytesPerSeed)
			fillRandom(randomNumberGenerator, fuzzBytes)

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
