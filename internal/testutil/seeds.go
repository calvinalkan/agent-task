package testutil

// Seed bundles a human-readable name with seed bytes.
//
// Curated seed sequences are hand-crafted to exercise specific scenarios that
// random fuzzing might take a long time to discover. Each seed is designed to
// produce a deterministic sequence of operations when fed to OpGenerator.
//
// Use RunBehaviorWithSeed to execute these:
//
//	testutil.RunBehaviorWithSeed(t, testutil.SeedBasicLifecycle(), cfg)
type Seed struct {
	Name string
	Data []byte
}

// CuratedSeeds returns all curated seeds with descriptive names.
func CuratedSeeds() []Seed {
	return []Seed{
		{Name: "basic_lifecycle", Data: SeedBasicLifecycle()},
		{Name: "blocker_chain", Data: SeedBlockerChain()},
		{Name: "blocked_start", Data: SeedBlockedStart()},
		{Name: "parent_child", Data: SeedParentChild()},
		{Name: "reopen_cycle", Data: SeedReopenCycle()},
		{Name: "invalid_inputs", Data: SeedInvalidInputs()},
		{Name: "mixed_operations", Data: SeedMixedOperations()},
		{Name: "priority_ordering", Data: SeedPriorityOrdering()},
		{Name: "deep_blocker_chain", Data: SeedDeepBlockerChain()},
	}
}

// defaultSeedConfig returns the config used for building curated seeds.
// This must match DefaultOpGenConfig() for seeds to work correctly.
func defaultSeedConfig() *OpGenConfig {
	cfg := DefaultOpGenConfig()

	return &cfg
}

// SeedBasicLifecycle returns a seed that exercises the basic ticket lifecycle:
// create → start → close → ls.
func SeedBasicLifecycle() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Update docs"}).
		Start("T0").
		Close("T0").
		LS(LSArgs{}).
		Bytes()
}

// SeedBlockerChain returns a seed that creates a blocker chain:
// A blocks B, B blocks C. Then resolves them in order.
func SeedBlockerChain() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Fix bug"}).
		Create(&CreateArgs{Title: "Add feature"}).
		Create(&CreateArgs{Title: "Refactor code"}).
		Block("T1", "T0"). // B blocked by A
		Block("T2", "T1"). // C blocked by B
		Start("T0").       // Start A
		Close("T0").       // Close A (unblocks B)
		Start("T1").       // Start B
		Close("T1").       // Close B (unblocks C)
		Start("T2").       // Start C
		Ready(0).
		Bytes()
}

// SeedBlockedStart returns a seed that attempts to start a blocked ticket.
func SeedBlockedStart() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Fix bug"}).
		Create(&CreateArgs{Title: "Add feature"}).
		Block("T1", "T0").
		Start("T1"). // Should error: blocked by T0
		Bytes()
}

// SeedParentChild returns a seed that creates parent-child relationships
// and tests the "parent must be started" rule.
func SeedParentChild() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Write tests"}).               // Parent
		Create(&CreateArgs{Title: "Review PR", ParentID: "T0"}). // Child
		Ready(0).                                                // Only parent should be ready
		Start("T0").                                             // Start parent
		Ready(0).                                                // Now child should be ready too
		Start("T1").                                             // Start child
		Close("T0").                                             // Try to close parent (should fail - child open)
		Close("T1").                                             // Close child first
		Close("T0").                                             // Now close parent
		Bytes()
}

// SeedReopenCycle returns a seed that exercises reopen:
// create → start → close → reopen → start → close.
func SeedReopenCycle() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Deploy app"}).
		Start("T0").
		Close("T0").
		Reopen("T0").
		Start("T0").
		Close("T0").
		LS(LSArgs{}).
		Bytes()
}

// SeedInvalidInputs returns a seed that exercises validation errors
// and error-returning transitions.
func SeedInvalidInputs() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Fix bug"}). // T0: valid create
		StartInvalid("nonexistent").           // Start nonexistent ticket
		Start("T0").                           // Start T0 (valid)
		Close("T0").                           // Close T0 (valid)
		Reopen("T0").                          // Reopen T0 (valid)
		Reopen("T0").                          // Reopen T0 again while open (invalid)
		Bytes()
}

// SeedMixedOperations returns a seed with a diverse mix of operations
// to exercise interleaved state changes.
func SeedMixedOperations() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Fix bug"}).
		Create(&CreateArgs{Title: "Add feature"}).
		Create(&CreateArgs{Title: "Refactor code"}).
		Block("T1", "T0"). // T1 blocked by T0
		Start("T0").
		Show("T0").
		LS(LSArgs{}).
		Ready(0).
		Close("T0"). // Unblocks T1
		Ready(0).
		Start("T1").
		LS(LSArgs{Status: "in_progress"}).
		Close("T1").
		Reopen("T0").
		Bytes()
}

// SeedPriorityOrdering returns a seed that creates tickets with different
// priorities to test ready queue ordering.
func SeedPriorityOrdering() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Fix bug", Priority: 3}).
		Create(&CreateArgs{Title: "Add feature", Priority: 1}).
		Create(&CreateArgs{Title: "Refactor code", Priority: 2}).
		Ready(0). // Should show P1, P2, P3 order
		Bytes()
}

// SeedDeepBlockerChain returns a seed that creates a longer blocker chain
// to test transitive blocking doesn't affect ready (only direct blockers matter).
func SeedDeepBlockerChain() []byte {
	cfg := defaultSeedConfig()

	return NewSeedBuilder(cfg).
		Create(&CreateArgs{Title: "Fix bug"}).
		Create(&CreateArgs{Title: "Add feature"}).
		Create(&CreateArgs{Title: "Refactor code"}).
		Create(&CreateArgs{Title: "Update docs"}).
		Block("T1", "T0"). // B blocked by A
		Block("T2", "T1"). // C blocked by B
		Block("T3", "T2"). // D blocked by C
		Ready(0).          // Only A should be ready
		Start("T0").
		Close("T0").
		Ready(0). // Now B should be ready
		Bytes()
}
