// File format correctness: fuzz testing
//
// Oracle: spec_oracle (internal/testutil/spec_oracle.go)
// Technique: coverage-guided fuzzing (go test -fuzz)
//
// These tests drive the API with fuzz-derived operations, then validate
// the on-disk file format. The spec_oracle independently parses the file and
// checks all format invariants (header CRC, slot layout, bucket integrity,
// probe sequences).
//
// NOTE: OpScan* operations include generated Filter specs, but filters are
// intentionally NOT applied in spec tests. Filters are client-side result
// filtering and don't affect the on-disk file format. Applying them would
// slow fuzz throughput without adding coverage for file format invariants.
//
// Failures here mean: "the file format violates the spec".

package slotcache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// specFuzzState holds the mutable state for a spec fuzz test run.
type specFuzzState struct {
	t             *testing.T
	cache         slotcache.Cache
	writer        slotcache.Writer
	seen          [][]byte
	options       slotcache.Options
	cacheFilePath string
}

// stateSnapshot captures Len and Scan results for comparison.
type stateSnapshot struct {
	length  int
	lenErr  error
	scan    []slotcache.Entry
	scanErr error
}

// validateFileFormat validates the on-disk format and fails if invalid.
func (s *specFuzzState) validateFileFormat(context string) {
	validationErr := testutil.ValidateFile(s.cacheFilePath, s.options)
	if validationErr != nil {
		s.t.Fatalf("speccheck failed %s: %s", context, testutil.DescribeSpecOracleError(validationErr))
	}
}

// snapshotState captures Len and Scan results for later comparison.
func (s *specFuzzState) snapshotState() stateSnapshot {
	length, lenErr := s.cache.Len()
	scan, scanErr := s.cache.Scan(slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0})

	return stateSnapshot{length: length, lenErr: lenErr, scan: scan, scanErr: scanErr}
}

// assertStateUnchanged verifies that Len and Scan haven't changed from the snapshot.
func (s *specFuzzState) assertStateUnchanged(context string, before stateSnapshot) {
	after := s.snapshotState()

	if before.lenErr == nil && after.lenErr == nil && before.length != after.length {
		s.t.Fatalf("%s changed Len(): before=%d after=%d", context, before.length, after.length)
	}

	if before.scanErr == nil && after.scanErr == nil {
		if diff := testutil.DiffEntries(before.scan, after.scan); diff != "" {
			s.t.Fatalf("%s changed Scan():\n%s", context, diff)
		}
	}
}

// handleCommit handles OpCommit with validation checkpoints.
func (s *specFuzzState) handleCommit() {
	if s.writer == nil {
		return
	}

	before := s.snapshotState()

	commitErr := s.writer.Commit()
	s.writer = nil

	// Validate file format after ANY commit attempt that should not corrupt.
	if commitErr == nil ||
		errors.Is(commitErr, slotcache.ErrWriteback) ||
		errors.Is(commitErr, slotcache.ErrFull) ||
		errors.Is(commitErr, slotcache.ErrOutOfOrderInsert) {
		s.validateFileFormat("after commit")
	}

	// Stronger check: ErrFull / ErrOutOfOrderInsert must not partially publish.
	if errors.Is(commitErr, slotcache.ErrFull) || errors.Is(commitErr, slotcache.ErrOutOfOrderInsert) {
		s.assertStateUnchanged("commit failed but", before)
	}
}

// handleWriterClose handles OpWriterClose with validation checkpoints.
func (s *specFuzzState) handleWriterClose() {
	if s.writer == nil {
		return
	}

	before := s.snapshotState()

	_ = s.writer.Close()
	s.writer = nil

	s.validateFileFormat("after Writer.Close()")
	s.assertStateUnchanged("Writer.Close()", before)
}

// FuzzSpec_GenerativeUsage drives the real API using fuzz-derived operations
// under a fixed, common configuration.
//
// Uses OpGenerator with SpecOpSet for operation generation, which includes
// all operations including Invalidate. The spec oracle validates the on-disk
// format at key checkpoints (reopen, commit, abort, invalidation).
func FuzzSpec_GenerativeUsage(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("commit"))
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_gen_fuzz.slc")
		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			UserVersion:  1,
			SlotCapacity: 64,
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

		const maximumSteps = 300

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
					state.validateFileFormat("after Close")

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
