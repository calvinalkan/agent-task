// Core behavior seed guard tests.
//
// These tests verify that the curated fuzz corpus seeds (A-H) still emit
// their intended operation sequences. They act as "tripwires" if the fuzz
// decoder's byte consumption changes.
//
// Purpose:
//   - Detect drift when decoder logic changes
//   - Ensure core behavior paths remain covered in the fuzz corpus
//   - Fail fast with clear messages if seeds need updating
//
// Each test validates MILESTONES (specific ops emitted in order), not full
// op-by-op traces, to reduce brittleness while catching meaningful changes.
//
// If a test fails, it means:
//  1. The seed bytes no longer trigger the intended code path, OR
//  2. The decoder's byte consumption changed
//
// Fix by updating the seed bytes in internal/testutil/behavior_seeds.go
// to match the current decoder behavior.
//
// WHY THIS LIVES IN pkg/slotcache (not internal/testutil):
// Guard tests must run with `go test ./pkg/slotcache` to catch regressions
// during normal development. Tests in internal/testutil/ only run when
// explicitly targeting that package, which isn't part of typical workflows.

package slotcache_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// -----------------------------------------------------------------------------
// Seed A: BasicHappyPath
// -----------------------------------------------------------------------------

func Test_CoreSeed_BasicHappyPath_Emits_WriteReadScan_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "basic.slc"))

	// Must emit: BeginWrite → Put → Commit → Get → Scan → ScanPrefix
	testutil.AssertSeedEmitsSequence(t, testutil.SeedBasicHappyPath, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit", "Get", "Scan", "ScanPrefix")
}

// -----------------------------------------------------------------------------
// Seed B: UpdateExistingKey
// -----------------------------------------------------------------------------

func Test_CoreSeed_UpdateExistingKey_Emits_TwoCommits_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "update.slc"))

	// Must emit two Put+Commit sequences, then Get and Scan
	testutil.AssertSeedEmitsSequence(t, testutil.SeedUpdateExistingKey, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit",
		"BeginWrite", "Writer.Put", "Writer.Commit",
		"Get", "Scan")
}

// -----------------------------------------------------------------------------
// Seed C: DeleteCommittedKey
// -----------------------------------------------------------------------------

func Test_CoreSeed_DeleteCommittedKey_Emits_PutDeleteLen_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "delete.slc"))

	// Must emit: Put→Commit, Delete→Commit, Get, Len
	testutil.AssertSeedEmitsSequence(t, testutil.SeedDeleteCommittedKey, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit",
		"BeginWrite", "Writer.Delete", "Writer.Commit",
		"Get", "Len")
}

// -----------------------------------------------------------------------------
// Seed D: CloseDiscardsBuffered
// -----------------------------------------------------------------------------

func Test_CoreSeed_CloseDiscardsBuffered_Emits_WriterClose_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "discard.slc"))

	// Must emit: committed Put, then uncommitted Put + WriterClose, then Scan
	testutil.AssertSeedEmitsSequence(t, testutil.SeedCloseDiscardsBuffered, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit",
		"BeginWrite", "Writer.Put", "Writer.Close",
		"Scan")
}

// -----------------------------------------------------------------------------
// Seed E: ErrBusyPaths
// -----------------------------------------------------------------------------

func Test_CoreSeed_ErrBusyPaths_Emits_CloseReopenWhileWriterActive_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "busy.slc"))

	// Must emit: BeginWrite, Close (ErrBusy), Reopen (ErrBusy), WriterClose, Close, Reopen, Len
	testutil.AssertSeedEmitsSequence(t, testutil.SeedErrBusyPaths, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Close", "Reopen", "Writer.Close", "Close", "Reopen", "Len")
}

// -----------------------------------------------------------------------------
// Seed F: InvalidInputs
// -----------------------------------------------------------------------------

func Test_CoreSeed_InvalidInputs_Emits_InvalidOpsAndCommit_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "invalid.slc"))

	// Must emit: invalid Get, invalid Scan, invalid ScanPrefix, then BeginWrite+Put+Commit
	testutil.AssertSeedEmitsSequence(t, testutil.SeedInvalidInputs, opts,
		testutil.DefaultMaxFuzzOperations,
		"Get", "Scan", "ScanPrefix", "BeginWrite", "Writer.Put", "Writer.Commit")
}

// -----------------------------------------------------------------------------
// Seed G: MultiKeyPersistence
// -----------------------------------------------------------------------------

func Test_CoreSeed_MultiKeyPersistence_Emits_TwoPutsReopenScan_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "multikey.slc"))

	// Must emit: two Puts in one session, Commit, Scan, Reopen, Scan, Get
	testutil.AssertSeedEmitsSequence(t, testutil.SeedMultiKeyPersistence, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Commit",
		"Scan", "Reopen", "Scan", "Get")
}

// -----------------------------------------------------------------------------
// Seed H: PrefixBehavior
// -----------------------------------------------------------------------------

func Test_CoreSeed_PrefixBehavior_Emits_MultipleScanPrefix_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "prefix.slc"))

	// Must emit: three Puts, Commit, then three ScanPrefix operations
	testutil.AssertSeedEmitsSequence(t, testutil.SeedPrefixBehavior, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Commit",
		"ScanPrefix", "ScanPrefix", "ScanPrefix")
}

// -----------------------------------------------------------------------------
// Combined: All core seeds emit BeginWrite
// -----------------------------------------------------------------------------

func Test_CoreSeeds_Emit_BeginWrite_When_Decoded(t *testing.T) {
	t.Parallel()

	// All core seeds should at minimum emit BeginWrite.
	// Note: ErrBusyPaths intentionally doesn't emit Writer.Put (tests error paths).
	for _, seed := range testutil.CoreBehaviorSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), seed.Name+".slc"))

			testutil.AssertSeedEmitsOps(t, seed.Data, opts, testutil.DefaultMaxFuzzOperations,
				"BeginWrite")
		})
	}
}
