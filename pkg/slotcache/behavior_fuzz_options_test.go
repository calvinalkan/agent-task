package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzBehavior_ModelVsReal_FuzzOptions is the behavior-level differential fuzzer,
// but with fuzz-derived Options to exercise alignment/padding, IndexSize==0,
// tiny capacities, and ordered/unordered mode.
func FuzzBehavior_ModelVsReal_FuzzOptions(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{7, 4, 0, 15, 0, 0x01, 0x02, 0x03}) // common config + some ops
	f.Add([]byte{0, 0, 0, 0, 1, 0xFF, 0xFF})        // smallest config, ordered
	f.Add([]byte("options+ops"))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "fuzz_opts.slc")

		options, rest := testutil.DeriveFuzzOptions(fuzzBytes, cacheFilePath)

		h := testutil.NewHarness(t, options)

		defer func() { _ = h.Real.Cache.Close() }()

		decoder := testutil.NewFuzzDecoder(rest, options)

		var previouslySeenKeys [][]byte

		for opIndex := 0; opIndex < maxFuzzOperations && decoder.HasMore(); opIndex++ {
			op := decoder.NextOp(h, previouslySeenKeys)

			modelResult := testutil.ApplyModel(h, op)
			realResult := testutil.ApplyReal(h, op)

			testutil.RememberPutKey(op, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, op, modelResult, realResult)
			testutil.CompareState(t, h)
		}
	})
}
