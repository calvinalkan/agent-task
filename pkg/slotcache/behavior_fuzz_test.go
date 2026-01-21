// Behavioral correctness: fuzz testing
//
// Oracle: in-memory behavioral model (internal/testutil/model)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// These tests verify that the real implementation's observable API behavior
// matches the simple in-memory model. They catch logic bugs in Get, Put,
// Delete, Scan, and transaction handling - but NOT file format issues.
//
// Failures here mean: "the API returned wrong results or wrong errors"

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// maxFuzzOperations is imported from testutil.DefaultMaxFuzzOperations
// to allow guard tests and seed helpers to share the same constant.

// FuzzBehavior_ModelVsReal is a coverage-guided fuzz test for public behavior.
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

		testHarness := testutil.NewHarness(t, options)

		defer func() {
			_ = testHarness.Real.Cache.Close()
		}()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		// We track keys that were successfully written at least once.
		// This increases the chance of hitting update/delete and prefix paths.
		var previouslySeenKeys [][]byte

		// Hard bound so one fuzz input cannot run forever.
		const maximumOperations = testutil.DefaultMaxFuzzOperations

		for operationIndex := 0; operationIndex < maximumOperations && decoder.HasMore(); operationIndex++ {
			nextOperation := decoder.NextOp(testHarness, previouslySeenKeys)

			modelResult := testutil.ApplyModel(testHarness, nextOperation)
			realResult := testutil.ApplyReal(testHarness, nextOperation)

			testutil.RememberPutKey(nextOperation, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, nextOperation, modelResult, realResult)
			testutil.CompareState(t, testHarness)
		}
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

		testHarness := testutil.NewHarness(t, options)

		defer func() {
			_ = testHarness.Real.Cache.Close()
		}()

		decoder := testutil.NewFuzzDecoder(fuzzBytes, options)

		var previouslySeenKeys [][]byte

		const maximumOperations = testutil.DefaultMaxFuzzOperations

		for operationIndex := 0; operationIndex < maximumOperations && decoder.HasMore(); operationIndex++ {
			nextOperation := decoder.NextOp(testHarness, previouslySeenKeys)

			modelResult := testutil.ApplyModel(testHarness, nextOperation)
			realResult := testutil.ApplyReal(testHarness, nextOperation)

			testutil.RememberPutKey(nextOperation, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, nextOperation, modelResult, realResult)
			testutil.CompareState(t, testHarness)
		}
	})
}
