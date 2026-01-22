package testutil_test

import (
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

// TestOpGenerator_ProtocolStability verifies that the OpGenerator byte→op
// mapping remains stable. If this test fails, the seed builder or OpGenerator
// protocol drifted and curated seeds may need updating.
func TestOpGenerator_ProtocolStability(t *testing.T) {
	t.Parallel()

	// Each seed defines an expected op sequence in its comment.
	// We verify all seeds decode to their expected ops.
	tests := []struct {
		name     string
		seed     []byte
		expected []string
	}{
		// Core behavior seeds
		{
			name:     "BasicHappyPath",
			seed:     testutil.SeedBasicHappyPath,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Commit", "Get", "Scan", "ScanPrefix"},
		},
		{
			name:     "UpdateExistingKey",
			seed:     testutil.SeedUpdateExistingKey,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.Put", "Writer.Commit", "Get", "Scan"},
		},
		{
			name:     "DeleteCommittedKey",
			seed:     testutil.SeedDeleteCommittedKey,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.Delete", "Writer.Commit", "Get", "Len"},
		},
		{
			name:     "CloseDiscardsBuffered",
			seed:     testutil.SeedCloseDiscardsBuffered,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.Put", "Writer.Close", "Scan"},
		},
		{
			name:     "ErrBusyPaths",
			seed:     testutil.SeedErrBusyPaths,
			expected: []string{"BeginWrite", "Close", "Reopen", "Writer.Close", "Close", "Reopen", "Len"},
		},
		{
			name:     "InvalidInputs",
			seed:     testutil.SeedInvalidInputs,
			expected: []string{"Get", "Scan", "ScanPrefix", "BeginWrite", "Writer.Put", "Writer.Commit"},
		},
		{
			name:     "MultiKeyPersistence",
			seed:     testutil.SeedMultiKeyPersistence,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Commit", "Scan", "Reopen", "Scan", "Get"},
		},
		{
			name:     "PrefixBehavior",
			seed:     testutil.SeedPrefixBehavior,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Commit", "ScanPrefix", "ScanPrefix", "ScanPrefix"},
		},
		// Filter seeds
		{
			name:     "FilteredScans",
			seed:     testutil.SeedFilteredScans,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Commit", "Scan", "Scan", "ScanPrefix"},
		},
		{
			name:     "FilterPagination",
			seed:     testutil.SeedFilterPagination,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Commit", "Scan"},
		},
		// UserHeader seeds
		{
			name:     "UserHeaderFlagsCommit",
			seed:     testutil.SeedUserHeaderFlagsCommit,
			expected: []string{"BeginWrite", "Writer.SetUserHeaderFlags", "Writer.Put", "Writer.Commit", "UserHeader"},
		},
		{
			name:     "UserHeaderDataDiscard",
			seed:     testutil.SeedUserHeaderDataDiscard,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.SetUserHeaderData", "Writer.Close", "UserHeader"},
		},
		{
			name:     "UserHeaderDataCommit",
			seed:     testutil.SeedUserHeaderDataCommit,
			expected: []string{"BeginWrite", "Writer.SetUserHeaderData", "Writer.Put", "Writer.Commit", "UserHeader"},
		},
		{
			name:     "UserHeaderBothCommit",
			seed:     testutil.SeedUserHeaderBothCommit,
			expected: []string{"BeginWrite", "Writer.SetUserHeaderFlags", "Writer.SetUserHeaderData", "Writer.Put", "Writer.Commit", "UserHeader"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := slotcache.Options{
				Path:         t.TempDir() + "/test.slc",
				KeySize:      8, // seedKeySize
				IndexSize:    4, // seedIndexSize
				SlotCapacity: 64,
			}

			cfg := testutil.CurratedSeedOpGenConfig()
			gen := testutil.NewOpGenerator(tc.seed, opts, &cfg)

			writerActive := false

			for i, want := range tc.expected {
				if !gen.HasMore() {
					t.Fatalf("generator exhausted at op %d, expected %s", i, want)
				}

				op := gen.NextOp(writerActive, nil)

				if op.Name() != want {
					t.Errorf("op[%d]: got %s, want %s", i, op.Name(), want)
				}

				// Track writer state.
				switch op.(type) {
				case testutil.OpBeginWrite:
					writerActive = true
				case testutil.OpCommit, testutil.OpWriterClose:
					writerActive = false
				}
			}
		})
	}
}

// TestOpGenerator_OrderedSeeds verifies ordered-keys seeds decode correctly.
func TestOpGenerator_OrderedSeeds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		seed     []byte
		expected []string
	}{
		{
			name:     "ScanRangeAll",
			seed:     testutil.SeedOrderedScanRangeAll,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Commit", "ScanRange"},
		},
		{
			name:     "OutOfOrderInsert",
			seed:     testutil.SeedOrderedOutOfOrderInsert,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.Put", "Writer.Commit"},
		},
		{
			name:     "ScanRangeBounded",
			seed:     testutil.SeedOrderedScanRangeBounded,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Commit", "ScanRange", "ScanRange"},
		},
		{
			name:     "ScanRangeReverse",
			seed:     testutil.SeedOrderedScanRangeReverse,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Commit", "ScanRange"},
		},
		{
			name:     "ScanPrefixOrdered",
			seed:     testutil.SeedOrderedScanPrefix,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Put", "Writer.Commit", "ScanPrefix"},
		},
		{
			name:     "FilterOrdered",
			seed:     testutil.SeedOrderedFilter,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Commit", "Scan", "ScanRange"},
		},
		{
			name:     "DeleteOrdered",
			seed:     testutil.SeedOrderedDelete,
			expected: []string{"BeginWrite", "Writer.Put", "Writer.Put", "Writer.Commit", "BeginWrite", "Writer.Delete", "Writer.Commit", "ScanRange"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := slotcache.Options{
				Path:         t.TempDir() + "/test.slc",
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 64,
				OrderedKeys:  true, // Required for ordered seeds
			}

			cfg := testutil.CurratedSeedOpGenConfig()
			gen := testutil.NewOpGenerator(tc.seed, opts, &cfg)

			writerActive := false

			for i, want := range tc.expected {
				if !gen.HasMore() {
					t.Fatalf("generator exhausted at op %d, expected %s", i, want)
				}

				op := gen.NextOp(writerActive, nil)

				if op.Name() != want {
					t.Errorf("op[%d]: got %s, want %s", i, op.Name(), want)
				}

				switch op.(type) {
				case testutil.OpBeginWrite:
					writerActive = true
				case testutil.OpCommit, testutil.OpWriterClose:
					writerActive = false
				}
			}
		})
	}
}
