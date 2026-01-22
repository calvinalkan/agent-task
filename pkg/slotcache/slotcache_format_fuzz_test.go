// Fuzz tests validating on-disk file format correctness.
// After each commit/close/reopen, validates that the file passes all format invariants.
//
// Failures mean: the file format is corrupted or invalid.

package slotcache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// Derives options from fuzz bytes to exercise alignment/padding edge cases,
// IndexSize==0, tiny capacities, and ordered/unordered modes.
func FuzzSlotcache_Format_Valid_When_Random_Ops_Applied_With_Derived_Options(f *testing.F) {
	// Seeds: encoded options + bytes for operations.
	commonOpts := testutil.OptionsToSeed(slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64})
	f.Add(append(commonOpts, 0x80, 0x80, 0x80, 0x80))

	tinyOrdered := testutil.OptionsToSeed(slotcache.Options{KeySize: 1, IndexSize: 0, SlotCapacity: 1, OrderedKeys: true})
	f.Add(append(tinyOrdered, 0x90, 0x91, 0x92, 0x93))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_fuzz.slc")

		options, rest := testutil.OptionsFromSeed(fuzzBytes, cacheFilePath)

		cache, openErr := slotcache.Open(options)
		if openErr != nil {
			t.Fatalf("Open failed unexpectedly: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		cfg := testutil.CurratedSeedOpGenConfig()
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
			state.applyOp(op)
		}

		if state.writer != nil {
			_ = state.writer.Close()
		}
	})
}

// Uses large KeySize (512) and IndexSize (16KB) to stress record-layout
// arithmetic and large key/index copy paths.
func FuzzSlotcache_Format_Valid_When_Random_Ops_Applied_With_Large_Records(f *testing.F) {
	// Seeds: raw bytes for operations (larger to allow multiple puts with 16KB index).
	f.Add([]byte{0x01, 0x02, 0x03, 0x04})
	f.Add([]byte("commit-ops"))
	f.Add(make([]byte, 64*1024))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_fuzz_large.slc")

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      512,
			IndexSize:    16 * 1024,
			UserVersion:  1,
			SlotCapacity: 64,
		}

		cache, openErr := slotcache.Open(options)
		if openErr != nil {
			t.Fatalf("Open failed unexpectedly: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		cfg := testutil.CurratedSeedOpGenConfig()
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
			state.applyOp(op)
		}

		if state.writer != nil {
			_ = state.writer.Close()
		}
	})
}

// specFuzzState holds the mutable state for a spec fuzz test run.
type specFuzzState struct {
	t             *testing.T
	cache         *slotcache.Cache
	writer        *slotcache.Writer
	seen          [][]byte
	options       slotcache.Options
	cacheFilePath string
}

// applyOp executes an operation and handles validation checkpoints.
func (s *specFuzzState) applyOp(op testutil.Operation) {
	switch typedOp := op.(type) {
	case testutil.OpReopen:
		closeErr := s.cache.Close()
		if errors.Is(closeErr, slotcache.ErrBusy) {
			return
		}

		var reopenErr error

		s.cache, reopenErr = slotcache.Open(s.options)
		if reopenErr != nil {
			s.t.Fatalf("reopen failed: %v", reopenErr)
		}

		s.validateFileFormat("after reopen")
		s.writer = nil

	case testutil.OpClose:
		closeErr := s.cache.Close()
		if closeErr == nil {
			s.validateFileFormat("after Close")

			var reopenErr error

			s.cache, reopenErr = slotcache.Open(s.options)
			if reopenErr != nil {
				s.t.Fatalf("reopen after Close failed: %v", reopenErr)
			}

			s.writer = nil
		}

	case testutil.OpInvalidate:
		invalidateErr := s.cache.Invalidate()
		if invalidateErr == nil || errors.Is(invalidateErr, slotcache.ErrInvalidated) {
			s.validateFileFormat("after Invalidate")

			_ = s.cache.Close()
			_ = os.Remove(s.cacheFilePath)

			var reopenErr error

			s.cache, reopenErr = slotcache.Open(s.options)
			if reopenErr != nil {
				s.t.Fatalf("reopen after invalidation failed: %v", reopenErr)
			}

			s.writer = nil
			s.seen = nil
		}

	case testutil.OpBeginWrite:
		if s.writer == nil {
			s.writer, _ = s.cache.Writer()
		}

	case testutil.OpLen:
		_, _ = s.cache.Len()

	case testutil.OpGet:
		_, _, _ = s.cache.Get(typedOp.Key)

	case testutil.OpScan:
		_, _ = s.cache.Scan(typedOp.Options)

	case testutil.OpScanPrefix:
		_, _ = s.cache.ScanPrefix(typedOp.Prefix, typedOp.Options)

	case testutil.OpScanMatch:
		_, _ = s.cache.ScanMatch(typedOp.Spec, typedOp.Options)

	case testutil.OpScanRange:
		_, _ = s.cache.ScanRange(typedOp.Start, typedOp.End, typedOp.Options)

	case testutil.OpUserHeader:
		_, _ = s.cache.UserHeader()

	case testutil.OpPut:
		if s.writer != nil {
			err := s.writer.Put(typedOp.Key, typedOp.Revision, typedOp.Index)
			if err == nil {
				testutil.RememberKey(typedOp.Key, s.options.KeySize, &s.seen)
			}
		}

	case testutil.OpDelete:
		if s.writer != nil {
			_, _ = s.writer.Delete(typedOp.Key)
		}

	case testutil.OpCommit:
		s.handleCommit()

	case testutil.OpWriterClose:
		s.handleWriterClose()

	case testutil.OpSetUserHeaderFlags:
		if s.writer != nil {
			_ = s.writer.SetUserHeaderFlags(typedOp.Flags)
		}

	case testutil.OpSetUserHeaderData:
		if s.writer != nil {
			_ = s.writer.SetUserHeaderData(typedOp.Data)
		}
	}
}

// validateFileFormat validates the on-disk format and fails if invalid.
func (s *specFuzzState) validateFileFormat(context string) {
	validationErr := testutil.ValidateFile(s.cacheFilePath, s.options)
	if validationErr != nil {
		s.t.Fatalf("format validation failed %s: %s", context, testutil.DescribeSpecOracleError(validationErr))
	}
}

// stateSnapshot captures Len and Scan results for comparison.
type stateSnapshot struct {
	length  int
	lenErr  error
	scan    []slotcache.Entry
	scanErr error
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

	s.validateFileFormat("after commit")

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

func Test_OptionsToSeed_RoundTrips_When_Encoded_And_Decoded(t *testing.T) {
	t.Parallel()

	tests := []slotcache.Options{
		{KeySize: 8, IndexSize: 4, SlotCapacity: 64, OrderedKeys: false},
		{KeySize: 1, IndexSize: 0, SlotCapacity: 1, OrderedKeys: true},
		{KeySize: 16, IndexSize: 0, SlotCapacity: 1, OrderedKeys: false},
		{KeySize: 7, IndexSize: 0, SlotCapacity: 4, OrderedKeys: false},
		{KeySize: 9, IndexSize: 3, SlotCapacity: 8, OrderedKeys: false},
		{KeySize: 32, IndexSize: 32, SlotCapacity: 128, OrderedKeys: true},
		{KeySize: 15, IndexSize: 7, SlotCapacity: 33, OrderedKeys: false},
	}

	for _, want := range tests {
		encoded := testutil.OptionsToSeed(want)
		got, _ := testutil.OptionsFromSeed(encoded, "/tmp/test.slc")

		if got.KeySize != want.KeySize {
			t.Errorf("KeySize: got %d, want %d (encoded: %v)", got.KeySize, want.KeySize, encoded)
		}

		if got.IndexSize != want.IndexSize {
			t.Errorf("IndexSize: got %d, want %d (encoded: %v)", got.IndexSize, want.IndexSize, encoded)
		}

		if got.SlotCapacity != want.SlotCapacity {
			t.Errorf("SlotCapacity: got %d, want %d (encoded: %v)", got.SlotCapacity, want.SlotCapacity, encoded)
		}

		if got.OrderedKeys != want.OrderedKeys {
			t.Errorf("OrderedKeys: got %v, want %v (encoded: %v)", got.OrderedKeys, want.OrderedKeys, encoded)
		}
	}
}
