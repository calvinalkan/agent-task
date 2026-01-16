// Behavioral correctness: deterministic seeded testing
//
// Oracle: in-memory behavioral model (internal/testutil/model)
// Technique: deterministic pseudo-random sequences (seeded PRNG)
//
// Same as behavior_fuzz_test.go but with fixed seeds for reproducibility.
// Each seed generates a deterministic operation sequence, making failures
// easy to reproduce without fuzzer corpus files. Runs on every CI build.
//
// Failures here mean: "the API returned wrong results or wrong errors"

package slotcache_test

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_Slotcache_Matches_Model_When_Random_Operations_Applied(t *testing.T) {
	t.Parallel()

	// Keep this deterministic for easy reproduction: seed N is the subtest name.
	seedCount := 50
	if testing.Short() {
		seedCount = 5
	}

	bytesPerSeed := 8192 // Enough for ~200 operations

	for seedIndex := range seedCount {
		seed := uint64(seedIndex + 1)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			temporaryDirectory := t.TempDir()
			cachePath := filepath.Join(temporaryDirectory, "test.slc")

			randomNumberGenerator := rand.New(rand.NewPCG(seed, seed))

			// Generate deterministic random bytes for this seed.
			fuzzBytes := make([]byte, bytesPerSeed)
			fillRandom(randomNumberGenerator, fuzzBytes)

			options := slotcache.Options{
				Path:         cachePath,
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			testHarness := testutil.NewHarness(t, options)

			defer func() {
				_ = testHarness.Real.Cache.Close()
			}()

			decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

			var previouslySeenKeys [][]byte

			for decoder.HasMore() {
				operationValue := decoder.NextOp(testHarness, previouslySeenKeys)

				modelResult := testutil.ApplyModel(testHarness, operationValue)
				realResult := testutil.ApplyReal(testHarness, operationValue)

				testutil.RememberPutKey(operationValue, modelResult, options.KeySize, &previouslySeenKeys)

				// Compare this operation's direct result.
				testutil.AssertOpMatch(t, operationValue, modelResult, realResult)

				// Compare the observable committed state.
				// This is useful even after errors: invalid inputs should not mutate state.
				testutil.CompareState(t, testHarness)
			}
		})
	}
}

func Test_Slotcache_Matches_Model_When_Random_Operations_Applied_In_OrderedKeys_Mode(t *testing.T) {
	t.Parallel()

	// Ordered mode is stricter about inserts, so we run fewer seeds but allow
	// a larger capacity to exercise ScanRange and ordered insert semantics.
	seedCount := 25
	if testing.Short() {
		seedCount = 5
	}

	bytesPerSeed := 8192

	for seedIndex := range seedCount {
		seed := uint64(10_000 + seedIndex + 1)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			temporaryDirectory := t.TempDir()
			cachePath := filepath.Join(temporaryDirectory, "test_ordered.slc")

			randomNumberGenerator := rand.New(rand.NewPCG(seed, seed))

			fuzzBytes := make([]byte, bytesPerSeed)
			fillRandom(randomNumberGenerator, fuzzBytes)

			options := slotcache.Options{
				Path:         cachePath,
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 256,
				OrderedKeys:  true,
			}

			testHarness := testutil.NewHarness(t, options)

			defer func() {
				_ = testHarness.Real.Cache.Close()
			}()

			decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

			var previouslySeenKeys [][]byte

			for decoder.HasMore() {
				operationValue := decoder.NextOp(testHarness, previouslySeenKeys)

				modelResult := testutil.ApplyModel(testHarness, operationValue)
				realResult := testutil.ApplyReal(testHarness, operationValue)

				testutil.RememberPutKey(operationValue, modelResult, options.KeySize, &previouslySeenKeys)

				testutil.AssertOpMatch(t, operationValue, modelResult, realResult)
				testutil.CompareState(t, testHarness)
			}
		})
	}
}
