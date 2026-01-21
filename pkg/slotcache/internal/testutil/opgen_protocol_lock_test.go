package testutil_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil/model"
)

// Test_CanonicalOpGenConfig_Matches_Frozen_Values_When_Called ensures the
// canonical configuration does not change accidentally. Any change to these
// values requires explicit seed migration and updating this test.
//
// WHY: Fuzz-seeded tests rely on deterministic operation generation. If the
// canonical config changes, existing curated seeds will generate different
// operation sequences, making them meaningless or causing false failures.
func Test_CanonicalOpGenConfig_Matches_Frozen_Values_When_Called(t *testing.T) {
	t.Parallel()

	cfg := testutil.CanonicalOpGenConfig()

	// Probability rates — these control operation distribution.
	assertEq(t, "InvalidKeyRate", 5, cfg.InvalidKeyRate)
	assertEq(t, "InvalidIndexRate", 5, cfg.InvalidIndexRate)
	assertEq(t, "InvalidScanOptsRate", 5, cfg.InvalidScanOptsRate)
	assertEq(t, "DeleteRate", 15, cfg.DeleteRate)
	assertEq(t, "CommitRate", 15, cfg.CommitRate)
	assertEq(t, "WriterCloseRate", 5, cfg.WriterCloseRate)
	assertEq(t, "NonMonotonicRate", 3, cfg.NonMonotonicRate)
	assertEq(t, "ReopenRate", 3, cfg.ReopenRate)
	assertEq(t, "CloseRate", 3, cfg.CloseRate)
	assertEq(t, "BeginWriteRate", 20, cfg.BeginWriteRate)

	// Key generation thresholds.
	assertEq(t, "KeyReuseMinThreshold", 4, cfg.KeyReuseMinThreshold)
	assertEq(t, "KeyReuseMaxThreshold", 32, cfg.KeyReuseMaxThreshold)

	// Scan options.
	assertEq(t, "SmallScanLimitBias", true, cfg.SmallScanLimitBias)

	// Phased generation — these control phase transitions.
	assertEq(t, "PhasedEnabled", true, cfg.PhasedEnabled)
	assertEq(t, "FillPhaseEnd", 60, cfg.FillPhaseEnd)
	assertEq(t, "ChurnPhaseEnd", 85, cfg.ChurnPhaseEnd)
	assertEq(t, "FillPhaseBeginWriteRate", 50, cfg.FillPhaseBeginWriteRate)
	assertEq(t, "FillPhaseCommitRate", 8, cfg.FillPhaseCommitRate)
	assertEq(t, "ChurnPhaseDeleteRate", 35, cfg.ChurnPhaseDeleteRate)
	assertEq(t, "ReadPhaseBeginWriteRate", 5, cfg.ReadPhaseBeginWriteRate)

	// AllowedOps defaults to zero (CoreOpSet behavior).
	assertEq(t, "AllowedOps", testutil.OpSet(0), cfg.AllowedOps)
}

// Test_OpSet_Contains_Expected_Ops_When_Predefined ensures the predefined
// OpSets contain expected ops. This catches accidental changes to the set
// compositions.
func Test_OpSet_Contains_Expected_Ops_When_Predefined(t *testing.T) {
	t.Parallel()

	// CoreOpSet should contain all currently-implemented operations.
	coreOps := []testutil.OpKind{
		testutil.OpKindLen,
		testutil.OpKindGet,
		testutil.OpKindScan,
		testutil.OpKindScanPrefix,
		testutil.OpKindScanMatch,
		testutil.OpKindScanRange,
		testutil.OpKindClose,
		testutil.OpKindReopen,
		testutil.OpKindBeginWrite,
		testutil.OpKindCommit,
		testutil.OpKindWriterClose,
		testutil.OpKindPut,
		testutil.OpKindDelete,
	}

	for _, op := range coreOps {
		if !testutil.CoreOpSet.Contains(op) {
			t.Errorf("CoreOpSet missing %s", op)
		}
	}

	// CoreOpSet should NOT contain Phase 1 ops.
	phase1Ops := []testutil.OpKind{
		testutil.OpKindUserHeader,
		testutil.OpKindSetUserHeaderFlags,
		testutil.OpKindSetUserHeaderData,
		testutil.OpKindInvalidate,
	}

	for _, op := range phase1Ops {
		if testutil.CoreOpSet.Contains(op) {
			t.Errorf("CoreOpSet should not contain %s (Phase 1 op)", op)
		}
	}

	// BehaviorOpSet should include UserHeader ops but not Invalidate.
	if !testutil.BehaviorOpSet.Contains(testutil.OpKindUserHeader) {
		t.Error("BehaviorOpSet should contain OpKindUserHeader")
	}

	if !testutil.BehaviorOpSet.Contains(testutil.OpKindSetUserHeaderFlags) {
		t.Error("BehaviorOpSet should contain OpKindSetUserHeaderFlags")
	}

	if !testutil.BehaviorOpSet.Contains(testutil.OpKindSetUserHeaderData) {
		t.Error("BehaviorOpSet should contain OpKindSetUserHeaderData")
	}

	if testutil.BehaviorOpSet.Contains(testutil.OpKindInvalidate) {
		t.Error("BehaviorOpSet should NOT contain OpKindInvalidate")
	}

	// SpecOpSet should include everything including Invalidate.
	if !testutil.SpecOpSet.Contains(testutil.OpKindInvalidate) {
		t.Error("SpecOpSet should contain OpKindInvalidate")
	}

	// SpecOpSet should be a superset of BehaviorOpSet.
	if testutil.SpecOpSet&testutil.BehaviorOpSet != testutil.BehaviorOpSet {
		t.Error("SpecOpSet should be a superset of BehaviorOpSet")
	}
}

// Test_OpGenerator_Produces_Stable_Sequence_When_Using_Canonical_Config ensures
// the first N operations generated from a fixed input under the canonical config
// with CoreOpSet remain stable.
//
// WHY: This is the "protocol lock" that ensures curated seeds remain meaningful.
// If this test fails, either:
//  1. The canonical config changed (update the frozen values test)
//  2. OpGenerator's byte→op logic changed (requires seed migration)
func Test_OpGenerator_Produces_Stable_Sequence_When_Using_Canonical_Config(t *testing.T) {
	t.Parallel()

	// Fixed seed that produces a reasonable op sequence.
	// This seed was chosen to exercise multiple op types.
	seed := []byte{
		0x50, 0x30, 0x20, 0x10, 0x00, // First few ops
		0x80, 0x90, 0xA0, 0xB0, 0xC0, // More bytes
		0x11, 0x22, 0x33, 0x44, 0x55, // Key/value data
		0x66, 0x77, 0x88, 0x99, 0xAA, // More data
		0xBB, 0xCC, 0xDD, 0xEE, 0xFF, // Trailing bytes
	}

	opts := slotcache.Options{
		Path:         filepath.Join(t.TempDir(), "protocol_lock.cache"),
		KeySize:      4,
		IndexSize:    2,
		SlotCapacity: 8,
	}

	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.CoreOpSet // Use only implemented ops

	gen := testutil.NewOpGenerator(seed, opts, &cfg)

	// Expected operation names for this seed under the canonical protocol.
	// If this changes, seeds need migration.
	expectedOps := []string{
		"BeginWrite",
		"Writer.Commit",
		"Reopen",
		"BeginWrite",
		"Writer.Put",
	}

	// Generate ops and track writer state manually for NextOp.
	harness := testutil.NewHarness(t, opts)

	defer func() { _ = harness.Real.Cache.Close() }()

	var seen [][]byte

	for i, expectedName := range expectedOps {
		if !gen.HasMore() {
			t.Fatalf("generator exhausted at op %d, expected %s", i, expectedName)
		}

		op := gen.NextOp(harness, seen)
		actualName := op.Name()

		if actualName != expectedName {
			t.Errorf("op[%d]: got %s, want %s", i, actualName, expectedName)
		}

		// Track seen keys for Put operations.
		if put, ok := op.(testutil.OpPut); ok {
			if len(put.Key) == opts.KeySize {
				seen = append(seen, put.Key)
			}
		}

		// Simulate writer state changes for accurate op generation.
		switch op.(type) {
		case testutil.OpBeginWrite:
			// Pretend writer is now active (affects next op selection).
			w, _ := harness.Model.Cache.BeginWrite()
			harness.Model.Writer = w

			rw, _ := harness.Real.Cache.BeginWrite()
			harness.Real.Writer = rw
		case testutil.OpCommit:
			if harness.Model.Writer != nil {
				_ = harness.Model.Writer.Commit()
			}

			if harness.Real.Writer != nil {
				_ = harness.Real.Writer.Commit()
			}

			harness.Model.Writer = nil
			harness.Real.Writer = nil
		case testutil.OpWriterClose:
			if harness.Model.Writer != nil {
				harness.Model.Writer.Close()
			}

			if harness.Real.Writer != nil {
				_ = harness.Real.Writer.Close()
			}

			harness.Model.Writer = nil
			harness.Real.Writer = nil
		case testutil.OpReopen:
			// Close and reopen the cache.
			_ = harness.Model.Cache.Close()
			_ = harness.Real.Cache.Close()

			harness.Model.Writer = nil
			harness.Real.Writer = nil

			// Reopen.
			harness.Model.Cache = model.Open(harness.Model.File)

			newCache, _ := slotcache.Open(opts)
			harness.Real.Cache = newCache
		case testutil.OpClose:
			_ = harness.Model.Cache.Close()
			_ = harness.Real.Cache.Close()

			harness.Model.Writer = nil
			harness.Real.Writer = nil
		}
	}
}

func assertEq[T comparable](t *testing.T, name string, want, got T) {
	t.Helper()

	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}
