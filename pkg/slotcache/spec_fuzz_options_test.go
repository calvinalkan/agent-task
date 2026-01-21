package slotcache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// FuzzSpec_GenerativeUsage_FuzzOptions is like FuzzSpec_GenerativeUsage, but
// derives slotcache.Options from the fuzz input so we exercise:
//   - key padding/alignment (KeySize != 8)
//   - IndexSize == 0
//   - tiny capacities (ErrFull, probe chains, tombstones)
//   - ordered/unordered mode
//
// Uses OpGenerator with SpecOpSet for operation generation.
func FuzzSpec_GenerativeUsage_FuzzOptions(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{7, 4, 0, 15, 0, 0x80, 0x80, 0x80}) // common-ish options + some actions
	f.Add([]byte{0, 0, 0, 0, 1, 0x80, 0x80, 0x80})  // tiny options + ordered

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_gen_fuzz_opts.slc")

		options, rest := testutil.DeriveFuzzOptions(fuzzBytes, cacheFilePath)

		cache, openErr := slotcache.Open(options)
		if openErr != nil {
			t.Fatalf("Open failed unexpectedly: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		// Use CanonicalOpGenConfig with SpecOpSet for spec fuzz tests.
		// SpecOpSet includes all operations including Invalidate.
		cfg := testutil.CanonicalOpGenConfig()
		cfg.AllowedOps = testutil.SpecOpSet
		opGen := testutil.NewOpGenerator(rest, options, &cfg)

		state := &specFuzzState{
			t:             t,
			cache:         cache,
			options:       options,
			cacheFilePath: cacheFilePath,
		}

		const maximumSteps = 250

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

		if state.writer != nil {
			_ = state.writer.Close()
		}
	})
}
