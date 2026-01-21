package testutil_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// -----------------------------------------------------------------------------
// SpecSeedBuilder protocol tests.
//
// These tests verify that the builder produces bytes compatible with the
// canonical OpGenerator protocol (CanonicalOpGenConfig + SpecOpSet).
// -----------------------------------------------------------------------------

// Test_SpecSeedBuilder_Emits_OpGenBytes_When_BeginWrite verifies BeginWrite protocol.
func Test_SpecSeedBuilder_Emits_OpGenBytes_When_BeginWrite(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().Build()

	// BeginWrite emits: roulette (0x80) + choice (0x00)
	// roulette=0x80 skips global ops, choice=0 → BeginWrite in reader mode
	if len(seed) != 2 {
		t.Fatalf("expected 2 bytes, got %d", len(seed))
	}

	// Verify via OpGenerator
	opts := slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64}
	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.SpecOpSet
	opGen := testutil.NewOpGenerator(seed, opts, &cfg)

	op := opGen.NextOp(false, nil)
	if _, ok := op.(testutil.OpBeginWrite); !ok {
		t.Errorf("expected OpBeginWrite, got %T", op)
	}
}

// Test_SpecSeedBuilder_Emits_OpGenBytes_When_Commit verifies Commit protocol.
func Test_SpecSeedBuilder_Emits_OpGenBytes_When_Commit(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().Commit().Build()

	// Verify via OpGenerator
	opts := slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64}
	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.SpecOpSet
	opGen := testutil.NewOpGenerator(seed, opts, &cfg)

	// First op: BeginWrite
	op1 := opGen.NextOp(false, nil)
	if _, ok := op1.(testutil.OpBeginWrite); !ok {
		t.Errorf("expected OpBeginWrite, got %T", op1)
	}

	// Second op: Commit (writer active)
	op2 := opGen.NextOp(true, nil)
	if _, ok := op2.(testutil.OpCommit); !ok {
		t.Errorf("expected OpCommit, got %T", op2)
	}
}

// Test_SpecSeedBuilder_Emits_OpGenBytes_When_Invalidate verifies Invalidate protocol.
func Test_SpecSeedBuilder_Emits_OpGenBytes_When_Invalidate(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).Invalidate().Build()

	// Invalidate emits: roulette in [closeThreshold, invalidateThreshold)
	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}

	// Verify via OpGenerator with SpecOpSet (which allows Invalidate)
	opts := slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64}
	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.SpecOpSet
	opGen := testutil.NewOpGenerator(seed, opts, &cfg)

	op := opGen.NextOp(false, nil)
	if _, ok := op.(testutil.OpInvalidate); !ok {
		t.Errorf("expected OpInvalidate, got %T", op)
	}
}

// Test_SpecSeedBuilder_Emits_OpGenBytes_When_SetUserHeaderFlags verifies SetUserHeaderFlags protocol.
func Test_SpecSeedBuilder_Emits_OpGenBytes_When_SetUserHeaderFlags(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().SetUserHeaderFlags(0x1234).Build()

	// Verify via OpGenerator
	opts := slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64}
	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.SpecOpSet
	opGen := testutil.NewOpGenerator(seed, opts, &cfg)

	// First op: BeginWrite
	_ = opGen.NextOp(false, nil)

	// Second op: SetUserHeaderFlags (writer active)
	op2 := opGen.NextOp(true, nil)
	if flagsOp, ok := op2.(testutil.OpSetUserHeaderFlags); !ok {
		t.Errorf("expected OpSetUserHeaderFlags, got %T", op2)
	} else if flagsOp.Flags != 0x1234 {
		t.Errorf("expected flags 0x1234, got 0x%x", flagsOp.Flags)
	}
}

// Test_SpecSeedBuilder_Emits_OpGenBytes_When_SetUserHeaderData verifies SetUserHeaderData protocol.
func Test_SpecSeedBuilder_Emits_OpGenBytes_When_SetUserHeaderData(t *testing.T) {
	t.Parallel()

	var testData [64]byte
	copy(testData[:], "test-data-payload")

	seed := testutil.NewSpecSeedBuilder(8, 4).BeginWrite().SetUserHeaderData(testData).Build()

	// Verify via OpGenerator
	opts := slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64}
	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.SpecOpSet
	opGen := testutil.NewOpGenerator(seed, opts, &cfg)

	// First op: BeginWrite
	_ = opGen.NextOp(false, nil)

	// Second op: SetUserHeaderData (writer active)
	op2 := opGen.NextOp(true, nil)
	if dataOp, ok := op2.(testutil.OpSetUserHeaderData); !ok {
		t.Errorf("expected OpSetUserHeaderData, got %T", op2)
	} else if dataOp.Data != testData {
		t.Error("data mismatch")
	}
}

// Test_SpecSeedBuilder_Emits_OpGenBytes_When_Reopen verifies Reopen protocol.
func Test_SpecSeedBuilder_Emits_OpGenBytes_When_Reopen(t *testing.T) {
	t.Parallel()

	seed := testutil.NewSpecSeedBuilder(8, 4).Reopen().Build()

	if len(seed) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(seed))
	}

	// Verify via OpGenerator
	opts := slotcache.Options{KeySize: 8, IndexSize: 4, SlotCapacity: 64}
	cfg := testutil.CanonicalOpGenConfig()
	cfg.AllowedOps = testutil.SpecOpSet
	opGen := testutil.NewOpGenerator(seed, opts, &cfg)

	op := opGen.NextOp(false, nil)
	if _, ok := op.(testutil.OpReopen); !ok {
		t.Errorf("expected OpReopen, got %T", op)
	}
}

// -----------------------------------------------------------------------------
// Curated seed structure tests.
// -----------------------------------------------------------------------------

// Test_SpecSeeds_Have_NonEmpty_Data_When_Built verifies all curated seeds are non-empty.
func Test_SpecSeeds_Have_NonEmpty_Data_When_Built(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.SpecFuzzSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			if len(seed.Data) == 0 {
				t.Errorf("seed %s has empty data", seed.Name)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// Milestone guard tests.
//
// These tests verify that curated seeds emit their intended operations.
// They act as tripwires if the protocol changes.
// -----------------------------------------------------------------------------

// Test_SpecSeed_Emits_Invalidate_When_InvalidateSeed verifies the Invalidate seed.
func Test_SpecSeed_Emits_Invalidate_When_InvalidateSeed(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "spec_invalidate.slc"))

	trace := testutil.RunSpecSeedTrace(t, testutil.SpecSeedInvalidate, opts, 50)

	// Must emit: BeginWrite → Put → Commit → Invalidate
	if !trace.HasOp(testutil.IsInvalidate) {
		t.Fatalf("seed did not emit Invalidate; got ops: %v", trace.OpNames())
	}

	// Verify sequence
	if !trace.HasOpSequence(
		testutil.IsBeginWrite,
		testutil.IsPut,
		testutil.IsCommit,
		testutil.IsInvalidate,
	) {
		t.Fatalf("seed did not emit expected sequence; got ops: %v", trace.OpNames())
	}
}

// Test_SpecSeed_Emits_SetUserHeaderFlags_When_UserHeaderFlagsSeed verifies the seed.
func Test_SpecSeed_Emits_SetUserHeaderFlags_When_UserHeaderFlagsSeed(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "spec_header_flags.slc"))

	trace := testutil.RunSpecSeedTrace(t, testutil.SpecSeedUserHeaderFlags, opts, 50)

	// Must emit: BeginWrite → SetUserHeaderFlags → Put → Commit → UserHeader
	if !trace.HasOp(testutil.IsSetUserHeaderFlags) {
		t.Fatalf("seed did not emit SetUserHeaderFlags; got ops: %v", trace.OpNames())
	}

	if !trace.HasOpSequence(
		testutil.IsBeginWrite,
		testutil.IsSetUserHeaderFlags,
		testutil.IsPut,
		testutil.IsCommit,
	) {
		t.Fatalf("seed did not emit expected sequence; got ops: %v", trace.OpNames())
	}
}

// Test_SpecSeed_Emits_SetUserHeaderData_When_UserHeaderDataSeed verifies the seed.
func Test_SpecSeed_Emits_SetUserHeaderData_When_UserHeaderDataSeed(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "spec_header_data.slc"))

	trace := testutil.RunSpecSeedTrace(t, testutil.SpecSeedUserHeaderData, opts, 50)

	// Must emit: BeginWrite → SetUserHeaderData → Put → Commit → Scan
	if !trace.HasOp(testutil.IsSetUserHeaderData) {
		t.Fatalf("seed did not emit SetUserHeaderData; got ops: %v", trace.OpNames())
	}

	if !trace.HasOpSequence(
		testutil.IsBeginWrite,
		testutil.IsSetUserHeaderData,
		testutil.IsPut,
		testutil.IsCommit,
	) {
		t.Fatalf("seed did not emit expected sequence; got ops: %v", trace.OpNames())
	}
}

// Test_SpecSeed_Emits_Both_UserHeader_Ops_When_UserHeaderBothSeed verifies the seed.
func Test_SpecSeed_Emits_Both_UserHeader_Ops_When_UserHeaderBothSeed(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "spec_header_both.slc"))

	trace := testutil.RunSpecSeedTrace(t, testutil.SpecSeedUserHeaderBoth, opts, 50)

	// Must emit both SetUserHeaderFlags and SetUserHeaderData
	if !trace.HasOp(testutil.IsSetUserHeaderFlags) {
		t.Fatalf("seed did not emit SetUserHeaderFlags; got ops: %v", trace.OpNames())
	}

	if !trace.HasOp(testutil.IsSetUserHeaderData) {
		t.Fatalf("seed did not emit SetUserHeaderData; got ops: %v", trace.OpNames())
	}

	if !trace.HasOpSequence(
		testutil.IsBeginWrite,
		testutil.IsSetUserHeaderFlags,
		testutil.IsSetUserHeaderData,
		testutil.IsPut,
		testutil.IsCommit,
	) {
		t.Fatalf("seed did not emit expected sequence; got ops: %v", trace.OpNames())
	}
}

// Test_SpecSeed_Emits_Reopen_And_Invalidate_When_InvalidateAfterReopenSeed verifies the seed.
func Test_SpecSeed_Emits_Reopen_And_Invalidate_When_InvalidateAfterReopenSeed(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "spec_invalidate_reopen.slc"))

	trace := testutil.RunSpecSeedTrace(t, testutil.SpecSeedInvalidateAfterReopen, opts, 50)

	// Must emit: BeginWrite → Put → Commit → Reopen → Invalidate
	if !trace.HasOp(testutil.IsReopen) {
		t.Fatalf("seed did not emit Reopen; got ops: %v", trace.OpNames())
	}

	if !trace.HasOp(testutil.IsInvalidate) {
		t.Fatalf("seed did not emit Invalidate; got ops: %v", trace.OpNames())
	}

	if !trace.HasOpSequence(
		testutil.IsBeginWrite,
		testutil.IsPut,
		testutil.IsCommit,
		testutil.IsReopen,
		testutil.IsInvalidate,
	) {
		t.Fatalf("seed did not emit expected sequence; got ops: %v", trace.OpNames())
	}
}

// Test_InvalidateSeeds_Emit_Invalidate_When_Decoded verifies all invalidate seeds.
func Test_InvalidateSeeds_Emit_Invalidate_When_Decoded(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.InvalidateSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), seed.Name+".slc"))
			trace := testutil.RunSpecSeedTrace(t, seed.Data, opts, 50)

			if !trace.HasOp(testutil.IsInvalidate) {
				t.Fatalf("seed %s did not emit Invalidate; got ops: %v", seed.Name, trace.OpNames())
			}
		})
	}
}

// Test_SpecUserHeaderSeeds_Emit_UserHeader_Ops_When_Decoded verifies all user header seeds.
func Test_SpecUserHeaderSeeds_Emit_UserHeader_Ops_When_Decoded(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.SpecUserHeaderSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), seed.Name+".slc"))
			trace := testutil.RunSpecSeedTrace(t, seed.Data, opts, 50)

			// Each seed should emit at least one of the user header ops
			hasFlags := trace.HasOp(testutil.IsSetUserHeaderFlags)
			hasData := trace.HasOp(testutil.IsSetUserHeaderData)

			if !hasFlags && !hasData {
				t.Fatalf("seed %s did not emit any user header ops; got ops: %v",
					seed.Name, trace.OpNames())
			}
		})
	}
}
