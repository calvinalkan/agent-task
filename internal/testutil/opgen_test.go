package testutil_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/calvinalkan/agent-task/internal/testutil"
	"github.com/calvinalkan/agent-task/internal/testutil/spec"
)

func Test_OpGenerator_Sets_CreateFields_When_Seeded(t *testing.T) {
	t.Parallel()

	model := seedModel(t, "a1", "a2")
	cfg := testutil.OpGenConfig{
		CreateRate:       100,
		InvalidInputRate: 0,
		InvalidIDRate:    0,
	}

	seed := testutil.NewSeedBuilder(&cfg).
		WithKnownIDs("a1", "a2").
		Create(&testutil.CreateArgs{
			Title:     "Update docs",
			Type:      "task",
			Priority:  2,
			ParentID:  "a2",
			BlockedBy: []string{"a1"},
		}).
		Bytes()

	gen := testutil.NewOpGenerator(seed, model, &cfg)
	op := gen.NextOp()

	create, ok := op.(*testutil.OpCreate)
	if !ok {
		t.Fatalf("expected OpCreate, got %T", op)
	}

	if create.Title != "Update docs" {
		t.Errorf("Title=%q, want %q", create.Title, "Update docs")
	}

	if create.Description != "" {
		t.Errorf("Description=%q, want empty", create.Description)
	}

	if create.Type != "task" {
		t.Errorf("Type=%q, want %q", create.Type, "task")
	}

	if create.Priority != 2 {
		t.Errorf("Priority=%d, want %d", create.Priority, 2)
	}

	if create.ParentID != "a2" {
		t.Errorf("ParentID=%q, want %q", create.ParentID, "a2")
	}

	if len(create.BlockedBy) != 1 || create.BlockedBy[0] != "a1" {
		t.Errorf("BlockedBy=%v, want [%q]", create.BlockedBy, "a1")
	}
}

func Test_OpGenerator_Uses_InvalidID_When_InvalidRateForces(t *testing.T) {
	t.Parallel()

	model := seedModel(t, "a1")
	cfg := testutil.OpGenConfig{
		StartRate:     100,
		InvalidIDRate: 100,
	}

	seed := testutil.NewSeedBuilder(&cfg).
		WithKnownIDs("a1").
		StartInvalid("INVALID-999").
		Bytes()

	gen := testutil.NewOpGenerator(seed, model, &cfg)
	op := gen.NextOp()

	start, ok := op.(testutil.OpStart)
	if !ok {
		t.Fatalf("expected OpStart, got %T", op)
	}

	if start.ID != "INVALID-999" {
		t.Errorf("ID=%q, want %q", start.ID, "INVALID-999")
	}
}

func Test_OpGenerator_Picks_BlockIDs_When_Seeded(t *testing.T) {
	t.Parallel()

	model := seedModel(t, "a1", "a2", "a3")
	cfg := testutil.OpGenConfig{
		BlockRate:     100,
		InvalidIDRate: 0,
	}

	seed := testutil.NewSeedBuilder(&cfg).
		WithKnownIDs("a1", "a2", "a3").
		Block("a2", "a3").
		Bytes()

	gen := testutil.NewOpGenerator(seed, model, &cfg)
	op := gen.NextOp()

	block, ok := op.(testutil.OpBlock)
	if !ok {
		t.Fatalf("expected OpBlock, got %T", op)
	}

	if block.ID != "a2" {
		t.Errorf("ID=%q, want %q", block.ID, "a2")
	}

	if block.BlockerID != "a3" {
		t.Errorf("BlockerID=%q, want %q", block.BlockerID, "a3")
	}
}

func Test_OpGenerator_Sets_LSFilters_When_Seeded(t *testing.T) {
	t.Parallel()

	model := seedModel(t, "a1")
	cfg := testutil.OpGenConfig{
		LSRate:           100,
		InvalidInputRate: 0,
	}

	seed := testutil.NewSeedBuilder(&cfg).
		LS(testutil.LSArgs{
			Status:   "in_progress",
			Priority: 3,
			Type:     "epic",
		}).
		Bytes()

	gen := testutil.NewOpGenerator(seed, model, &cfg)
	op := gen.NextOp()

	lsOp, ok := op.(testutil.OpLS)
	if !ok {
		t.Fatalf("expected OpLS, got %T", op)
	}

	if lsOp.Status != "in_progress" {
		t.Errorf("Status=%q, want %q", lsOp.Status, "in_progress")
	}

	if lsOp.Priority != 3 {
		t.Errorf("Priority=%d, want %d", lsOp.Priority, 3)
	}

	if lsOp.Type != "epic" {
		t.Errorf("Type=%q, want %q", lsOp.Type, "epic")
	}

	if lsOp.Limit != 0 || lsOp.Offset != 0 {
		t.Errorf("Limit/Offset=%d/%d, want 0/0", lsOp.Limit, lsOp.Offset)
	}
}

func Test_OpGenerator_Sets_ReadyLimit_When_Seeded(t *testing.T) {
	t.Parallel()

	model := seedModel(t, "a1")
	cfg := testutil.OpGenConfig{
		ReadyRate: 100,
	}

	seed := testutil.NewSeedBuilder(&cfg).
		Ready(4).
		Bytes()

	gen := testutil.NewOpGenerator(seed, model, &cfg)
	op := gen.NextOp()

	ready, ok := op.(testutil.OpReady)
	if !ok {
		t.Fatalf("expected OpReady, got %T", op)
	}

	if ready.Limit != 4 {
		t.Errorf("Limit=%d, want %d", ready.Limit, 4)
	}
}

// Test_OpGenerator_Produces_Expected_Ops_When_Curated_Seed_Applied verifies that
// the curated seeds produce the expected operation structs with correct fields.
// If this test fails, either the seed bytes or OpGenerator protocol drifted.
func Test_OpGenerator_Produces_Expected_Ops_When_Curated_Seed_Applied(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		seed     []byte
		expected []testutil.Op
	}{
		{
			name: "basic_lifecycle",
			seed: testutil.SeedBasicLifecycle(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Update docs"},
				testutil.OpStart{ID: "T0"},
				testutil.OpClose{ID: "T0"},
				testutil.OpLS{},
			},
		},
		{
			name: "blocker_chain",
			seed: testutil.SeedBlockerChain(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Fix bug"},
				&testutil.OpCreate{Title: "Add feature"},
				&testutil.OpCreate{Title: "Refactor code"},
				testutil.OpBlock{ID: "T1", BlockerID: "T0"},
				testutil.OpBlock{ID: "T2", BlockerID: "T1"},
				testutil.OpStart{ID: "T0"},
				testutil.OpClose{ID: "T0"},
				testutil.OpStart{ID: "T1"},
				testutil.OpClose{ID: "T1"},
				testutil.OpStart{ID: "T2"},
				testutil.OpReady{},
			},
		},
		{
			name: "blocked_start",
			seed: testutil.SeedBlockedStart(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Fix bug"},
				&testutil.OpCreate{Title: "Add feature"},
				testutil.OpBlock{ID: "T1", BlockerID: "T0"},
				testutil.OpStart{ID: "T1"},
			},
		},
		{
			name: "parent_child",
			seed: testutil.SeedParentChild(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Write tests"},
				&testutil.OpCreate{Title: "Review PR", ParentID: "T0"},
				testutil.OpReady{},
				testutil.OpStart{ID: "T0"},
				testutil.OpReady{},
				testutil.OpStart{ID: "T1"},
				testutil.OpClose{ID: "T0"},
				testutil.OpClose{ID: "T1"},
				testutil.OpClose{ID: "T0"},
			},
		},
		{
			name: "reopen_cycle",
			seed: testutil.SeedReopenCycle(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Deploy app"},
				testutil.OpStart{ID: "T0"},
				testutil.OpClose{ID: "T0"},
				testutil.OpReopen{ID: "T0"},
				testutil.OpStart{ID: "T0"},
				testutil.OpClose{ID: "T0"},
				testutil.OpLS{},
			},
		},
		{
			name: "invalid_inputs",
			seed: testutil.SeedInvalidInputs(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Fix bug"},
				testutil.OpStart{ID: "nonexistent"},
				testutil.OpStart{ID: "T0"},
				testutil.OpClose{ID: "T0"},
				testutil.OpReopen{ID: "T0"},
				testutil.OpReopen{ID: "T0"},
			},
		},
		{
			name: "mixed_operations",
			seed: testutil.SeedMixedOperations(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Fix bug"},
				&testutil.OpCreate{Title: "Add feature"},
				&testutil.OpCreate{Title: "Refactor code"},
				testutil.OpBlock{ID: "T1", BlockerID: "T0"},
				testutil.OpStart{ID: "T0"},
				testutil.OpShow{ID: "T0"},
				testutil.OpLS{},
				testutil.OpReady{},
				testutil.OpClose{ID: "T0"},
				testutil.OpReady{},
				testutil.OpStart{ID: "T1"},
				testutil.OpLS{Status: "in_progress"},
				testutil.OpClose{ID: "T1"},
				testutil.OpReopen{ID: "T0"},
			},
		},
		{
			name: "priority_ordering",
			seed: testutil.SeedPriorityOrdering(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Fix bug", Priority: 3},
				&testutil.OpCreate{Title: "Add feature", Priority: 1},
				&testutil.OpCreate{Title: "Refactor code", Priority: 2},
				testutil.OpReady{},
			},
		},
		{
			name: "deep_blocker_chain",
			seed: testutil.SeedDeepBlockerChain(),
			expected: []testutil.Op{
				&testutil.OpCreate{Title: "Fix bug"},
				&testutil.OpCreate{Title: "Add feature"},
				&testutil.OpCreate{Title: "Refactor code"},
				&testutil.OpCreate{Title: "Update docs"},
				testutil.OpBlock{ID: "T1", BlockerID: "T0"},
				testutil.OpBlock{ID: "T2", BlockerID: "T1"},
				testutil.OpBlock{ID: "T3", BlockerID: "T2"},
				testutil.OpReady{},
				testutil.OpStart{ID: "T0"},
				testutil.OpClose{ID: "T0"},
				testutil.OpReady{},
			},
		},
	}

	// Ignore CreatedID since it's set during test execution, not by the generator.
	cmpOpts := cmp.Options{
		cmpopts.IgnoreFields(testutil.OpCreate{}, "CreatedID"),
	}

	cfg := testutil.DefaultOpGenConfig()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			model := spec.New()
			clock := testutil.NewClock()
			harness := &testutil.Harness{Model: model, Clock: clock}
			gen := testutil.NewOpGenerator(tc.seed, model, &cfg)
			createIndex := 0

			for i, want := range tc.expected {
				if !gen.HasMore() {
					t.Fatalf("generator exhausted at op %d, expected %T", i, want)
				}

				op := gen.NextOp()

				// Apply creates to model so subsequent ops can reference IDs.
				if create, ok := op.(*testutil.OpCreate); ok {
					create.CreatedID = fmt.Sprintf("T%d", createIndex)
					createIndex++

					if res := create.ApplyModel(harness); !res.OK {
						t.Fatalf("create op[%d] failed to apply to model: %v", i, res.Err)
					}
				} else {
					_ = op.ApplyModel(harness)
				}

				if diff := cmp.Diff(want, op, cmpOpts...); diff != "" {
					t.Errorf("op[%d] mismatch (-want +got):\n%s", i, diff)
				}
			}
		})
	}
}

func seedModel(t *testing.T, ids ...string) *spec.Model {
	t.Helper()

	model := spec.New()
	base := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

	for i, id := range ids {
		user := &spec.UserCreateInput{
			Title:   fmt.Sprintf("Ticket %d", i+1),
			Content: "content",
		}
		fuzz := spec.FuzzCreateInput{
			ID:        id,
			CreatedAt: base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
		}

		_, err := model.Create(user, fuzz)
		if err != nil {
			t.Fatalf("seedModel create %s: %v", id, err)
		}
	}

	return model
}
