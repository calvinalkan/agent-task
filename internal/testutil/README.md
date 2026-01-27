# testutil — tk test infrastructure

Test-only infrastructure for `internal/cli/*_test.go` and behavior testing.

## What this package provides

- **Behavior testing**: Compare the real CLI against an in-memory spec model to verify ticket operations.
- **Deterministic fuzzing**: Convert arbitrary bytes into reproducible operation sequences.
- **Curated seeds**: Pre-built byte sequences that exercise important code paths (blockers, parent-child, lifecycle).

## Files

```
testutil/
├── bytestream.go       # ByteStream: deterministic byte reader for fuzz inputs
├── clock.go            # Clock: monotonic timestamps for spec model
├── ops.go              # Operation types (OpCreate, OpStart, etc.) and results
├── opgen.go            # OpGenerator: bytes → operations using configurable weights
│
├── harness.go          # Harness: wires CLI + Model + Clock together
├── run.go              # RunBehavior/RunBehaviorWithSeed: execute and compare
├── errmatch.go         # ErrorBucket: fuzzy error matching for CLI stderr
│
├── seeds.go            # CuratedSeeds: pre-built scenarios (lifecycle, blockers, etc.)
├── seed_builder.go     # SeedBuilder: fluent API for creating seeds
│
└── spec/
    └── spec.go         # Model: in-memory behavioral oracle (source of truth)
```

## Behavior testing flow

```
RunBehaviorWithSeed(tb, seed, cfg)
    │
    ├── NewHarness(tb)
    │     ├── Model: in-memory spec (testutil/spec)
    │     ├── CLI: real tk commands via cli.NewCLI(t)
    │     └── Clock: deterministic timestamps
    │
    └── for each op from OpGenerator:
          ├── op = gen.NextOp()
          ├── realResult = op.ApplyReal(harness)   # runs CLI
          ├── modelResult = op.ApplyModel(harness) # updates spec
          ├── compareResults(op, modelResult, realResult)
          └── CompareState(harness) at checkpoints
```

## SeedBuilder + OpGenerator flow

The `SeedBuilder` encodes operations into bytes without knowing actual ticket IDs.
It uses placeholder names (T0, T1, T2...) that map to indexes:

```
SeedBuilder                    Bytes                 OpGenerator + Model
───────────────────────────────────────────────────────────────────────
Create("Fix bug")      →   [0, 10, 0, ...]    →   OpCreate{Title:"Fix bug"}
  (tracks T0)                                       model.IDs() = ["<real-id>"]

Start("T0")            →   [20, 15, 0]        →   OpStart{ID: model.IDs()[0]}
  (index 0)                                         = OpStart{ID: "<real-id>"}

Block("T1", "T0")      →   [50, 15, 1, 15, 0] →   OpBlock{ID: "T1", BlockerID: "T0"}
  (index 1, index 0)
```

## Curated seeds

Pre-built seeds exercise specific scenarios:

| Seed | Scenario |
|------|----------|
| `SeedBasicLifecycle` | create → start → close → ls |
| `SeedBlockerChain` | A blocks B, B blocks C, resolve in order |
| `SeedBlockedStart` | attempt to start a blocked ticket |
| `SeedParentChild` | parent-child with "parent must be started" rule |
| `SeedReopenCycle` | create → start → close → reopen → start → close |
| `SeedInvalidInputs` | validation errors (nonexistent IDs, invalid states) |
| `SeedMixedOperations` | interleaved state changes |
| `SeedPriorityOrdering` | tickets with different priorities |
| `SeedDeepBlockerChain` | longer blocker chain (A→B→C→D) |

## Usage

See `internal/cli/state_model_test.go` for real-world usage:

```go
// Run all curated seeds against CLI + Model
func Test_CLI_Matches_Model_When_Curated_Seed_Applied(t *testing.T) {
    for _, seed := range testutil.CuratedSeeds() {
        t.Run(seed.Name, func(t *testing.T) {
            cfg := testutil.DefaultRunConfig()
            cfg.MaxOps = 150
            cfg.CompareStateEveryN = 10

            testutil.RunBehaviorWithSeed(t, seed.Data, cfg)
        })
    }
}

// Run random fuzz bytes against CLI + Model
func Test_CLI_Matches_Model_When_Seeded_Random_Ops_Applied(t *testing.T) {
    rng := rand.New(rand.NewPCG(seed, seed))
    fuzzBytes := make([]byte, 4096)
    fillRandom(rng, fuzzBytes)

    cfg := testutil.DefaultRunConfig()
    testutil.RunBehaviorWithSeed(t, fuzzBytes, cfg)
}
```

## Spec model

The `spec.Model` in `testutil/spec/spec.go` is the **source of truth** for correct behavior.
If the real CLI disagrees with the spec, the CLI is wrong.