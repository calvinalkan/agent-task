//go:build slotcache_impl

package slotcache_test

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// This file contains the core *state-model property test*.
//
// It is deterministic (seeded) and uses the fuzz operation decoder.
func Test_Slotcache_Matches_Model_Property(t *testing.T) {
	// Keep this deterministic for easy reproduction: seed N is the subtest name.
	seedCount := 50
	bytesPerSeed := 8192 // Enough for ~200 operations

	for seedIndex := 0; seedIndex < seedCount; seedIndex++ {
		seed := int64(seedIndex + 1)

		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()

			temporaryDirectory := t.TempDir()
			cachePath := filepath.Join(temporaryDirectory, "test.slc")

			randomNumberGenerator := rand.New(rand.NewSource(seed))

			// Generate deterministic random bytes for this seed.
			fuzzBytes := make([]byte, bytesPerSeed)
			randomNumberGenerator.Read(fuzzBytes)

			options := slotcache.Options{
				Path:         cachePath,
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
			}

			testHarness := newHarness(t, options)
			defer func() {
				_ = testHarness.real.cache.Close()
			}()

			decoder := newFuzzOperationDecoder(fuzzBytes, options)

			var previouslySeenKeys [][]byte

			for decoder.hasMoreBytes() {
				operationValue := decoder.nextOperation(testHarness, previouslySeenKeys)

				modelResult := applyModel(testHarness, operationValue)
				realResult := applyReal(testHarness, operationValue)

				rememberKeyAfterSuccessfulPutIfValid(operationValue, modelResult, options.KeySize, &previouslySeenKeys)

				// Compare this operation's direct result.
				assertMatch(t, operationValue, modelResult, realResult)

				// Compare the observable committed state.
				// This is useful even after errors: invalid inputs should not mutate state.
				compareObservableState(t, testHarness)
			}
		})
	}
}
