// UserHeader seed guard tests.
//
// These tests verify that the curated fuzz corpus seeds for user header
// coverage still emit their intended operation sequences.
//
// Purpose:
//   - Acts as a "tripwire" if the fuzz decoder's byte consumption changes
//   - Ensures user header coverage is maintained in the fuzz corpus
//   - Fails fast with a clear message if seeds need updating
//
// If a test fails, it means:
//  1. The seed bytes no longer trigger the user header code path, OR
//  2. The decoder's byte consumption for user header ops changed
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
// Seed K: UserHeaderFlagsCommit
// -----------------------------------------------------------------------------

func Test_UserHeaderSeed_FlagsCommit_Emits_SetFlagsAndUserHeader_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "header_flags.slc"))

	// Must emit: BeginWrite → SetUserHeaderFlags → Put → Commit → UserHeader
	testutil.AssertSeedEmitsSequence(t, testutil.SeedUserHeaderFlagsCommit, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.SetUserHeaderFlags", "Writer.Put", "Writer.Commit", "UserHeader")
}

// -----------------------------------------------------------------------------
// Seed L: UserHeaderDataDiscard
// -----------------------------------------------------------------------------

func Test_UserHeaderSeed_DataDiscard_Emits_SetDataAndWriterClose_When_Decoded(t *testing.T) {
	t.Parallel()

	opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), "header_data.slc"))

	// Must emit: BeginWrite → Put → Commit → BeginWrite → SetUserHeaderData → WriterClose → UserHeader
	testutil.AssertSeedEmitsSequence(t, testutil.SeedUserHeaderDataDiscard, opts,
		testutil.DefaultMaxFuzzOperations,
		"BeginWrite", "Writer.Put", "Writer.Commit",
		"BeginWrite", "Writer.SetUserHeaderData", "Writer.Close",
		"UserHeader")
}

// -----------------------------------------------------------------------------
// Combined: All UserHeader seeds emit their key operations
// -----------------------------------------------------------------------------

func Test_UserHeaderSeeds_Emit_UserHeader_When_Decoded(t *testing.T) {
	t.Parallel()

	// All user header seeds should emit the UserHeader read operation.
	for _, seed := range testutil.UserHeaderSeeds() {
		t.Run(seed.Name, func(t *testing.T) {
			t.Parallel()

			opts := testutil.DefaultGuardOptions(filepath.Join(t.TempDir(), seed.Name+".slc"))

			testutil.AssertSeedEmitsOps(t, seed.Data, opts, testutil.DefaultMaxFuzzOperations,
				"UserHeader")
		})
	}
}
