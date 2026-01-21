// Guard helpers for seed assertion testing.
//
// These helpers run curated seeds through the decoder and verify they emit
// intended operations. Used to detect drift when decoder logic changes.
//
// Guard tests are intentionally lightweight:
// - Check milestones (specific ops emitted), not full traces
// - Fail fast with clear messages when seeds need updating
// - Keep wrapper tests thin (testutil does the work)

package testutil

import (
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// -----------------------------------------------------------------------------
// Trace recording
// -----------------------------------------------------------------------------

// TraceEntry records a single emitted operation and its results.
type TraceEntry struct {
	Op          Operation
	ModelResult OperationResult
	RealResult  OperationResult
}

// SeedTrace records all operations from running a seed.
type SeedTrace struct {
	Entries []TraceEntry
}

// RunSeedTrace executes a seed through the decoder and records all operations.
// It also validates model vs real behavior for each operation.
func RunSeedTrace(tb testing.TB, seed []byte, opts slotcache.Options, maxOps int) *SeedTrace {
	tb.Helper()

	harness := NewHarness(tb, opts)
	tb.Cleanup(func() {
		_ = harness.Real.Cache.Close()
	})

	decoder := NewFuzzDecoder(seed, opts)

	var previouslySeenKeys [][]byte

	entries := make([]TraceEntry, 0, maxOps)

	for i := 0; i < maxOps && decoder.HasMore(); i++ {
		op := decoder.NextOp(harness, previouslySeenKeys)

		modelResult := ApplyModel(harness, op)
		realResult := ApplyReal(harness, op)

		RememberPutKey(op, modelResult, opts.KeySize, &previouslySeenKeys)

		// Validate behavior matches while recording.
		AssertOpMatch(tb, op, modelResult, realResult)

		entries = append(entries, TraceEntry{
			Op:          op,
			ModelResult: modelResult,
			RealResult:  realResult,
		})
	}

	return &SeedTrace{Entries: entries}
}

// -----------------------------------------------------------------------------
// Trace inspection helpers
// -----------------------------------------------------------------------------

// HasOp returns true if the trace contains an operation matching the predicate.
func (t *SeedTrace) HasOp(pred func(Operation) bool) bool {
	for _, e := range t.Entries {
		if pred(e.Op) {
			return true
		}
	}

	return false
}

// CountOps returns the number of operations matching the predicate.
func (t *SeedTrace) CountOps(pred func(Operation) bool) int {
	count := 0

	for _, e := range t.Entries {
		if pred(e.Op) {
			count++
		}
	}

	return count
}

// HasOpSequence returns true if the trace contains operations matching
// the predicates in order (not necessarily consecutive).
func (t *SeedTrace) HasOpSequence(preds ...func(Operation) bool) bool {
	if len(preds) == 0 {
		return true
	}

	predIndex := 0

	for _, e := range t.Entries {
		if preds[predIndex](e.Op) {
			predIndex++
			if predIndex == len(preds) {
				return true
			}
		}
	}

	return false
}

// FilteredScanCount returns the number of scan operations with non-nil filters.
func (t *SeedTrace) FilteredScanCount() int {
	return t.CountOps(IsFilteredScan)
}

// GetLastResult returns the last result for an operation matching the predicate.
// Returns nil if no matching operation was found.
func (t *SeedTrace) GetLastResult(pred func(Operation) bool) (Operation, OperationResult) {
	for i := len(t.Entries) - 1; i >= 0; i-- {
		if pred(t.Entries[i].Op) {
			return t.Entries[i].Op, t.Entries[i].RealResult
		}
	}

	return nil, nil
}

// OpNames returns the sequence of operation names in the trace.
func (t *SeedTrace) OpNames() []string {
	names := make([]string, len(t.Entries))
	for i, e := range t.Entries {
		names[i] = e.Op.Name()
	}

	return names
}

// -----------------------------------------------------------------------------
// Operation predicates
// -----------------------------------------------------------------------------

// IsFilteredScan returns true if the operation is a scan with a non-nil filter.
func IsFilteredScan(op Operation) bool {
	switch v := op.(type) {
	case OpScan:
		return v.Filter != nil
	case OpScanPrefix:
		return v.Filter != nil
	case OpScanMatch:
		return v.Filter != nil
	case OpScanRange:
		return v.Filter != nil
	default:
		return false
	}
}

// IsOpType returns a predicate that matches operations by type name.
func IsOpType(name string) func(Operation) bool {
	return func(op Operation) bool {
		return op.Name() == name
	}
}

// IsBeginWrite returns true if the operation is BeginWrite.
func IsBeginWrite(op Operation) bool {
	_, ok := op.(OpBeginWrite)

	return ok
}

// IsPut returns true if the operation is Writer.Put.
func IsPut(op Operation) bool {
	_, ok := op.(OpPut)

	return ok
}

// IsDelete returns true if the operation is Writer.Delete.
func IsDelete(op Operation) bool {
	_, ok := op.(OpDelete)

	return ok
}

// IsCommit returns true if the operation is Writer.Commit.
func IsCommit(op Operation) bool {
	_, ok := op.(OpCommit)

	return ok
}

// IsWriterClose returns true if the operation is Writer.Close.
func IsWriterClose(op Operation) bool {
	_, ok := op.(OpWriterClose)

	return ok
}

// IsGet returns true if the operation is Get.
func IsGet(op Operation) bool {
	_, ok := op.(OpGet)

	return ok
}

// IsScan returns true if the operation is Scan.
func IsScan(op Operation) bool {
	_, ok := op.(OpScan)

	return ok
}

// IsScanPrefix returns true if the operation is ScanPrefix.
func IsScanPrefix(op Operation) bool {
	_, ok := op.(OpScanPrefix)

	return ok
}

// IsClose returns true if the operation is Cache.Close.
func IsClose(op Operation) bool {
	_, ok := op.(OpClose)

	return ok
}

// IsReopen returns true if the operation is Reopen.
func IsReopen(op Operation) bool {
	_, ok := op.(OpReopen)

	return ok
}

// IsLen returns true if the operation is Len.
func IsLen(op Operation) bool {
	_, ok := op.(OpLen)

	return ok
}

// IsPutWithKey returns a predicate that matches Put ops with a specific key.
func IsPutWithKey(key []byte) func(Operation) bool {
	return func(op Operation) bool {
		put, ok := op.(OpPut)
		if !ok {
			return false
		}

		return bytesEqual(put.Key, key)
	}
}

// IsGetWithKey returns a predicate that matches Get ops with a specific key.
func IsGetWithKey(key []byte) func(Operation) bool {
	return func(op Operation) bool {
		get, ok := op.(OpGet)
		if !ok {
			return false
		}

		return bytesEqual(get.Key, key)
	}
}

// IsDeleteWithKey returns a predicate that matches Delete ops with a specific key.
func IsDeleteWithKey(key []byte) func(Operation) bool {
	return func(op Operation) bool {
		del, ok := op.(OpDelete)
		if !ok {
			return false
		}

		return bytesEqual(del.Key, key)
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}

// -----------------------------------------------------------------------------
// Assertion helpers
// -----------------------------------------------------------------------------

// AssertSeedEmitsFilteredScan runs a seed and fails if no filtered scan is emitted.
func AssertSeedEmitsFilteredScan(tb testing.TB, seed []byte, opts slotcache.Options, maxOps int) {
	tb.Helper()

	trace := RunSeedTrace(tb, seed, opts, maxOps)

	if trace.FilteredScanCount() == 0 {
		tb.Fatalf("seed emitted no scan with Filter != nil; update seed bytes")
	}
}

// AssertSeedEmitsOps runs a seed and fails if it doesn't emit all required op types.
func AssertSeedEmitsOps(tb testing.TB, seed []byte, opts slotcache.Options, maxOps int, opNames ...string) {
	tb.Helper()

	trace := RunSeedTrace(tb, seed, opts, maxOps)

	for _, name := range opNames {
		if !trace.HasOp(IsOpType(name)) {
			tb.Fatalf("seed did not emit required op %q; got ops: %v", name, trace.OpNames())
		}
	}
}

// AssertSeedEmitsSequence runs a seed and fails if it doesn't emit ops in the given order.
func AssertSeedEmitsSequence(tb testing.TB, seed []byte, opts slotcache.Options, maxOps int, opNames ...string) {
	tb.Helper()

	trace := RunSeedTrace(tb, seed, opts, maxOps)

	preds := make([]func(Operation) bool, len(opNames))
	for i, name := range opNames {
		preds[i] = IsOpType(name)
	}

	if !trace.HasOpSequence(preds...) {
		tb.Fatalf("seed did not emit required op sequence %v; got ops: %v", opNames, trace.OpNames())
	}
}

// -----------------------------------------------------------------------------
// Default test options (for guard tests that don't need custom config)
// -----------------------------------------------------------------------------

// DefaultGuardOptions returns the standard options used for seed guard tests.
func DefaultGuardOptions(path string) slotcache.Options {
	return slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
	}
}
