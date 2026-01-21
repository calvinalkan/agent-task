package slotcache_test

// Near-cap fuzz targets
//
// These fuzz tests run slotcache under a fixed "near-cap" configuration:
//   - KeySize=512
//   - IndexSize=16KiB
//   - SlotCapacity=64
//
// This configuration is intentionally NOT near the file-size/capacity caps; it is
// chosen to be large enough to stress record-layout arithmetic (padding/offsets),
// large key/index copy paths, and scan/filtering behavior, while still being cheap
// enough for fuzzing.
//
// Why a separate fuzz target (instead of extending the existing fuzz option
// generator)?
//   - The existing fuzz-option generator intentionally keeps sizes small to
//     maximize fuzz iteration throughput.
//   - Large per-entry records (like 16KiB indexes) drastically reduce the number
//     of operations the fuzzer can execute per second.
//   - Keeping this in a separate fuzz target gives it an independent corpus and
//     makes it easy to run in isolation via -fuzz / FUZZ_TARGET.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// Near-cap (but still safe) configuration used to exercise:
//   - large KeySize (max)
//   - large per-entry index bytes (moderately large, but well within caps)
//
// This is intentionally NOT near the file-size or capacity caps, so it stays
// cheap enough for fuzzing while still covering large-record logic.
const (
	nearCapKeySize   = 512
	nearCapIndexSize = 16 * 1024
	nearCapCapacity  = 64
)

// FuzzSpec_GenerativeUsage_NearCapConfig drives the real API under the near-cap
// configuration and validates the on-disk file against the spec_oracle.
//
// Uses OpGenerator with SpecOpSet for operation generation, which includes
// all operations including Invalidate. The spec oracle validates the on-disk
// format at key checkpoints (reopen, commit, abort, invalidation).
func FuzzSpec_GenerativeUsage_NearCapConfig(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("commit"))
	f.Add(make([]byte, 64))
	// Big enough to allow multiple Put operations with a 16KiB index.
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_gen_fuzz_nearcap.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      nearCapKeySize,
			IndexSize:    nearCapIndexSize,
			UserVersion:  1,
			SlotCapacity: nearCapCapacity,
		}

		cache, openErr := slotcache.Open(options)
		if openErr != nil {
			t.Fatalf("Open failed unexpectedly: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		// Use CanonicalOpGenConfig with SpecOpSet for spec fuzz tests.
		// SpecOpSet includes all operations including Invalidate.
		cfg := testutil.CanonicalOpGenConfig()
		cfg.AllowedOps = testutil.SpecOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

		state := &specFuzzState{
			t:             t,
			cache:         cache,
			options:       options,
			cacheFilePath: cacheFilePath,
		}

		const maximumSteps = 251

		for stepIndex := 0; stepIndex < maximumSteps && opGen.HasMore(); stepIndex++ {
			writerActive := state.writer != nil
			op := opGen.NextOp(writerActive, state.seen)

			// Apply the operation and handle validation checkpoints.
			switch typedOp := op.(type) {
			case testutil.OpReopen:
				closeErr := state.cache.Close()
				if errors.Is(closeErr, slotcache.ErrBusy) {
					continue
				}

				var reopenErr error

				state.cache, reopenErr = slotcache.Open(options)
				if reopenErr != nil {
					t.Fatalf("reopen failed: %v", reopenErr)
				}

				state.validateFileFormat("after reopen")
				state.writer = nil

			case testutil.OpClose:
				closeErr := state.cache.Close()
				if closeErr == nil {
					var reopenErr error

					state.cache, reopenErr = slotcache.Open(options)
					if reopenErr != nil {
						t.Fatalf("reopen after Close failed: %v", reopenErr)
					}

					state.writer = nil
				}

			case testutil.OpInvalidate:
				invalidateErr := state.cache.Invalidate()
				if invalidateErr == nil || errors.Is(invalidateErr, slotcache.ErrInvalidated) {
					state.validateFileFormat("after Invalidate")

					// Reset: close, delete file, recreate.
					_ = state.cache.Close()
					_ = os.Remove(cacheFilePath)

					var reopenErr error

					state.cache, reopenErr = slotcache.Open(options)
					if reopenErr != nil {
						t.Fatalf("reopen after invalidation failed: %v", reopenErr)
					}

					state.writer = nil
					state.seen = nil
				}

			case testutil.OpBeginWrite:
				if state.writer == nil {
					state.writer, _ = state.cache.BeginWrite()
				}

			case testutil.OpLen:
				_, _ = state.cache.Len()

			case testutil.OpGet:
				_, _, _ = state.cache.Get(typedOp.Key)

			case testutil.OpScan:
				_, _ = state.cache.Scan(typedOp.Options)

			case testutil.OpScanPrefix:
				_, _ = state.cache.ScanPrefix(typedOp.Prefix, typedOp.Options)

			case testutil.OpScanMatch:
				_, _ = state.cache.ScanMatch(typedOp.Spec, typedOp.Options)

			case testutil.OpScanRange:
				_, _ = state.cache.ScanRange(typedOp.Start, typedOp.End, typedOp.Options)

			case testutil.OpUserHeader:
				_, _ = state.cache.UserHeader()

			case testutil.OpPut:
				if state.writer != nil {
					err := state.writer.Put(typedOp.Key, typedOp.Revision, typedOp.Index)
					if err == nil && len(typedOp.Key) == options.KeySize {
						state.seen = append(state.seen, append([]byte(nil), typedOp.Key...))
					}
				}

			case testutil.OpDelete:
				if state.writer != nil {
					_, _ = state.writer.Delete(typedOp.Key)
				}

			case testutil.OpCommit:
				state.handleCommit()

			case testutil.OpWriterClose:
				state.handleWriterClose()

			case testutil.OpSetUserHeaderFlags:
				if state.writer != nil {
					_ = state.writer.SetUserHeaderFlags(typedOp.Flags)
				}

			case testutil.OpSetUserHeaderData:
				if state.writer != nil {
					_ = state.writer.SetUserHeaderData(typedOp.Data)
				}
			}
		}

		// If the fuzzer left a writer open, abort it.
		if state.writer != nil {
			_ = state.writer.Close()
		}
	})
}

func FuzzBehavior_ModelVsReal_NearCapConfig(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	// Big enough to allow multiple Put operations with a 16KiB index.
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "behavior_fuzz_nearcap.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      nearCapKeySize,
			IndexSize:    nearCapIndexSize,
			SlotCapacity: nearCapCapacity,
		}

		h := testutil.NewHarness(t, options)

		defer func() { _ = h.Real.Cache.Close() }()

		cfg := testutil.CanonicalOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

		var previouslySeenKeys [][]byte

		for opIndex := 0; opIndex < testutil.DefaultMaxFuzzOperations && opGen.HasMore(); opIndex++ {
			writerActive := h.Model.Writer != nil && h.Real.Writer != nil
			op := opGen.NextOp(writerActive, previouslySeenKeys)

			modelResult := testutil.ApplyModel(h, op)
			realResult := testutil.ApplyReal(h, op)

			testutil.RememberPutKey(op, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, op, modelResult, realResult)
			testutil.CompareState(t, h)
		}
	})
}

func FuzzBehavior_ModelVsReal_NearCapConfig_OrderedKeys(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0xFF})
	// Big enough to allow multiple Put operations with a 16KiB index.
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "behavior_fuzz_nearcap_ordered.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      nearCapKeySize,
			IndexSize:    nearCapIndexSize,
			SlotCapacity: nearCapCapacity,
			OrderedKeys:  true,
		}

		h := testutil.NewHarness(t, options)

		defer func() { _ = h.Real.Cache.Close() }()

		cfg := testutil.CanonicalOpGenConfig()
		cfg.AllowedOps = testutil.BehaviorOpSet
		opGen := testutil.NewOpGenerator(fuzzBytes, options, &cfg)

		var previouslySeenKeys [][]byte

		for opIndex := 0; opIndex < testutil.DefaultMaxFuzzOperations && opGen.HasMore(); opIndex++ {
			writerActive := h.Model.Writer != nil && h.Real.Writer != nil
			op := opGen.NextOp(writerActive, previouslySeenKeys)

			modelResult := testutil.ApplyModel(h, op)
			realResult := testutil.ApplyReal(h, op)

			testutil.RememberPutKey(op, modelResult, options.KeySize, &previouslySeenKeys)

			testutil.AssertOpMatch(t, op, modelResult, realResult)
			testutil.CompareState(t, h)
		}
	})
}
