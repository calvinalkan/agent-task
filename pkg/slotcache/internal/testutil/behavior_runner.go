package testutil

import (
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// BehaviorRunConfig configures a model-vs-real behavior test run.
type BehaviorRunConfig struct {
	// MaxOps is the maximum number of operations to execute.
	MaxOps int

	// LightCompareEveryN runs a light state comparison every N operations.
	// Set to 0 to disable light comparisons.
	LightCompareEveryN int

	// HeavyCompareEveryN runs a full CompareState every N operations.
	// Set to 0 to disable cadence-based heavy comparisons.
	HeavyCompareEveryN int

	// CompareOnCommit triggers a full CompareState after Writer.Commit.
	CompareOnCommit bool

	// CompareOnCloseReopen triggers a full CompareState after Close/Reopen.
	CompareOnCloseReopen bool
}

// OpSource produces operations for RunBehavior.
type OpSource interface {
	NextOp(h *Harness, seen [][]byte) Operation
}

// RunBehavior executes a deterministic stream of operations and compares
// the public API behavior between the model and the real cache.
func RunBehavior(tb testing.TB, opts slotcache.Options, src OpSource, cfg BehaviorRunConfig) {
	tb.Helper()

	if cfg.MaxOps <= 0 {
		tb.Fatalf("RunBehavior requires MaxOps > 0")
	}

	harness := NewHarness(tb, opts)
	tb.Cleanup(func() {
		_ = harness.Real.Cache.Close()
	})

	var previouslySeenKeys [][]byte

	for opIndex := 1; opIndex <= cfg.MaxOps; opIndex++ {
		operationValue := src.NextOp(harness, previouslySeenKeys)

		modelResult := ApplyModel(harness, operationValue)
		realResult := ApplyReal(harness, operationValue)

		RememberPutKey(operationValue, modelResult, opts.KeySize, &previouslySeenKeys)

		AssertOpMatch(tb, operationValue, modelResult, realResult)

		if shouldRunHeavyCompare(operationValue, opIndex, cfg) {
			CompareState(tb, harness)

			continue
		}

		if shouldRunLightCompare(opIndex, cfg) {
			compareStateLight(tb, harness)
		}
	}
}

func shouldRunHeavyCompare(operationValue Operation, opIndex int, cfg BehaviorRunConfig) bool {
	if cfg.HeavyCompareEveryN > 0 && opIndex%cfg.HeavyCompareEveryN == 0 {
		return true
	}

	switch operationValue.(type) {
	case OpCommit:
		return cfg.CompareOnCommit
	case OpClose, OpReopen:
		return cfg.CompareOnCloseReopen
	default:
		return false
	}
}

func shouldRunLightCompare(opIndex int, cfg BehaviorRunConfig) bool {
	return cfg.LightCompareEveryN > 0 && opIndex%cfg.LightCompareEveryN == 0
}

// compareStateLight performs a cheaper state comparison than CompareState.
func compareStateLight(tb testing.TB, harness *Harness) {
	tb.Helper()

	mLen, mLenErr := harness.Model.Cache.Len()
	rLen, rLenErr := harness.Real.Cache.Len()

	if !errorsMatch(mLenErr, rLenErr) {
		tb.Fatalf("Len() error mismatch\nmodel=%v\nreal=%v", mLenErr, rLenErr)
	}

	if mLenErr != nil {
		return
	}

	if mLen != rLen {
		tb.Fatalf("Len() value mismatch\nmodel=%d\nreal=%d", mLen, rLen)
	}

	expectedEntries := mLen
	fwdOpts := slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: 0}
	revOpts := slotcache.ScanOptions{Reverse: true, Offset: 0, Limit: 0}

	harness.Scratch.modelFwd = ensureEntriesCapacity(harness.Scratch.modelFwd, expectedEntries)
	harness.Scratch.realFwd = ensureEntriesCapacity(harness.Scratch.realFwd, expectedEntries)

	mFwd, mFwdErr := scanModelInto(tb, harness.Model.Cache, fwdOpts, harness.Scratch.modelFwd)
	rFwd, rFwdErr := scanRealInto(tb, harness.Real.Cache, fwdOpts, harness.Scratch.realFwd)

	harness.Scratch.modelFwd = mFwd
	harness.Scratch.realFwd = rFwd

	if !errorsMatch(mFwdErr, rFwdErr) {
		tb.Fatalf("Scan(forward) error mismatch\nmodel=%v\nreal=%v", mFwdErr, rFwdErr)
	}

	if diff := DiffEntries(mFwd, rFwd); diff != "" {
		tb.Fatalf("Scan(forward) entries mismatch (-model +real):\n%s", diff)
	}

	if mLen != len(mFwd) {
		tb.Fatalf("model: Len()=%d but Scan(forward) returned %d entries", mLen, len(mFwd))
	}

	if rLen != len(rFwd) {
		tb.Fatalf("real: Len()=%d but Scan(forward) returned %d entries", rLen, len(rFwd))
	}

	harness.Scratch.modelRev = ensureEntriesCapacity(harness.Scratch.modelRev, expectedEntries)
	harness.Scratch.realRev = ensureEntriesCapacity(harness.Scratch.realRev, expectedEntries)

	mRev, mRevErr := scanModelInto(tb, harness.Model.Cache, revOpts, harness.Scratch.modelRev)
	rRev, rRevErr := scanRealInto(tb, harness.Real.Cache, revOpts, harness.Scratch.realRev)

	harness.Scratch.modelRev = mRev
	harness.Scratch.realRev = rRev

	if !errorsMatch(mRevErr, rRevErr) {
		tb.Fatalf("Scan(reverse) error mismatch\nmodel=%v\nreal=%v", mRevErr, rRevErr)
	}

	if diff := DiffEntries(mRev, rRev); diff != "" {
		tb.Fatalf("Scan(reverse) entries mismatch (-model +real):\n%s", diff)
	}

	if !entriesAreReverse(mFwd, mRev) {
		tb.Fatal("model: reverse scan is not the exact reverse of forward scan")
	}

	if !entriesAreReverse(rFwd, rRev) {
		tb.Fatal("real: reverse scan is not the exact reverse of forward scan")
	}
}
