package testutil_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// These tests verify the guard helper infrastructure works correctly.
// They use the known-working FilteredScans seed as the reference.
//
// NOTE: Some core behavior seeds (A-H) have incorrect byte encoding and
// will be fixed separately. The guard helpers are designed to catch
// exactly this kind of drift.

func Test_RunSeedTrace_Captures_Operations_When_Seed_Is_Executed(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))

	// Use FilteredScans which has verified correct encoding.
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	if len(trace.Entries) == 0 {
		t.Fatal("expected trace to capture operations")
	}

	// FilteredScans should emit at minimum: BeginWrite, Put, Commit.
	if !trace.HasOp(testutil.IsBeginWrite) {
		t.Fatal("expected trace to contain BeginWrite")
	}

	if !trace.HasOp(testutil.IsPut) {
		t.Fatal("expected trace to contain Put")
	}

	if !trace.HasOp(testutil.IsCommit) {
		t.Fatalf("expected trace to contain Commit; got ops: %v", trace.OpNames())
	}
}

func Test_SeedTrace_HasOp_Returns_True_When_Operation_Exists(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	if !trace.HasOp(testutil.IsBeginWrite) {
		t.Fatal("expected HasOp to find BeginWrite")
	}

	if !trace.HasOp(testutil.IsPut) {
		t.Fatal("expected HasOp to find Put")
	}

	if !trace.HasOp(testutil.IsScan) {
		t.Fatal("expected HasOp to find Scan in FilteredScans")
	}
}

func Test_SeedTrace_CountOps_Returns_Correct_Count_When_Operations_Exist(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	// FilteredScans has 2 Put operations.
	putCount := trace.CountOps(testutil.IsPut)
	if putCount != 2 {
		t.Fatalf("expected 2 Put ops in FilteredScans, got %d", putCount)
	}

	// Should have at least 1 commit.
	commitCount := trace.CountOps(testutil.IsCommit)
	if commitCount < 1 {
		t.Fatalf("expected at least 1 Commit op, got %d", commitCount)
	}
}

func Test_SeedTrace_HasOpSequence_Returns_False_When_Order_Is_Wrong(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	// Correct order: BeginWrite -> Put -> Commit should exist.
	if !trace.HasOpSequence(testutil.IsBeginWrite, testutil.IsPut, testutil.IsCommit) {
		t.Fatal("expected sequence BeginWrite -> Put -> Commit to be found")
	}

	// Wrong order should fail: Commit before BeginWrite makes no sense.
	if trace.HasOpSequence(testutil.IsCommit, testutil.IsBeginWrite) {
		t.Fatal("expected reversed sequence not to be found")
	}
}

func Test_SeedTrace_FilteredScanCount_Returns_Zero_When_No_Filters(t *testing.T) {
	t.Parallel()

	// Create a minimal seed that doesn't use filters.
	// Just BeginWrite -> Close (no filtered scans).
	minimalSeed := []byte{
		0x80, 0x04, // BeginWrite (roulette=0x80, choice=0x04 <20)
		0x80, 0x55, // WriterClose (choice=0x55=85 in [75,85))
		0x80, 0x64, // Len (choice=0x64=100, >=90)
	}

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	trace := testutil.RunSeedTrace(t, minimalSeed, opts, testutil.DefaultMaxFuzzOperations)

	if trace.FilteredScanCount() != 0 {
		t.Fatalf("minimal seed should have 0 filtered scans, got %d", trace.FilteredScanCount())
	}
}

func Test_SeedTrace_FilteredScanCount_Returns_NonZero_When_Filters_Present(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))

	trace := testutil.RunSeedTrace(t, testutil.SeedFilteredScans, opts, testutil.DefaultMaxFuzzOperations)
	if trace.FilteredScanCount() == 0 {
		t.Fatal("FilteredScans seed should have > 0 filtered scans")
	}
}

func Test_AssertSeedEmitsFilteredScan_Passes_When_Filter_Seeds_Used(t *testing.T) {
	t.Parallel()

	for _, seed := range testutil.FilterSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), seed.Name+".slc"))

			// This should not panic or fail.
			testutil.AssertSeedEmitsFilteredScan(t, seed.Data, opts, testutil.DefaultMaxFuzzOperations)
		})
	}
}

func Test_AssertSeedEmitsOps_Passes_When_Required_Ops_Present(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	// FilteredScans definitely has these ops.
	testutil.AssertSeedEmitsOps(t, seed, opts, testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit", "Scan")
}

func Test_AssertSeedEmitsSequence_Passes_When_Sequence_Correct(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	// Should pass with correct sequence.
	testutil.AssertSeedEmitsSequence(t, seed, opts, testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit")
}

func Test_SeedTrace_GetLastResult_Returns_Result_When_Operation_Found(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	op, result := trace.GetLastResult(testutil.IsScan)
	if op == nil {
		t.Fatal("expected to find Scan operation")
	}

	if result == nil {
		t.Fatal("expected Scan result to be non-nil")
	}

	// Verify it's actually a scan result.
	if _, ok := result.(testutil.ResScan); !ok {
		t.Fatalf("expected ResScan, got %T", result)
	}
}

func Test_SeedTrace_GetLastResult_Returns_Nil_When_Operation_Not_Found(t *testing.T) {
	t.Parallel()

	// Minimal seed without ScanRange.
	minimalSeed := []byte{
		0x80, 0x04, // BeginWrite
		0x80, 0x55, // WriterClose
	}

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	trace := testutil.RunSeedTrace(t, minimalSeed, opts, testutil.DefaultMaxFuzzOperations)

	op, result := trace.GetLastResult(func(o testutil.Operation) bool {
		_, ok := o.(testutil.OpScanRange)

		return ok
	})

	if op != nil || result != nil {
		t.Fatal("expected nil when ScanRange not found")
	}
}

func Test_IsOpType_Matches_By_Name_When_Name_Exists(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	if !trace.HasOp(testutil.IsOpType("BeginWrite")) {
		t.Fatal("IsOpType should find BeginWrite")
	}

	if trace.HasOp(testutil.IsOpType("NonExistent")) {
		t.Fatal("IsOpType should not find NonExistent")
	}
}

func Test_OpNames_Returns_All_Operation_Names_When_Called(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "test.slc"))
	seed := testutil.SeedFilteredScans

	trace := testutil.RunSeedTrace(t, seed, opts, testutil.DefaultMaxFuzzOperations)

	names := trace.OpNames()

	if len(names) != len(trace.Entries) {
		t.Fatalf("OpNames length %d != Entries length %d", len(names), len(trace.Entries))
	}

	// First op should be BeginWrite.
	if names[0] != "BeginWrite" {
		t.Fatalf("expected first op to be BeginWrite, got %s", names[0])
	}
}
