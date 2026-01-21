# slotcache fast-test generation plan

## Goals
- Centralize **behavior test generation logic** in `pkg/slotcache/internal/testutil` so fast tests can reuse it with different configurations.
- Improve **fast deterministic coverage** without sacrificing reproducibility.
- Add **regression guards** for hand-rolled seed bytes (like `behavior_filter_seed_guard_test.go`).
- Keep fuzz targets **separate** for fixed-config vs option-derivation (separate corpora) while reducing duplication.

## Current context (how things work today)

### Test categories
1) **Behavior correctness (model vs real)**
   - Files: `behavior_*`, `behavior_fuzz_options_test.go`, `near_cap_fuzz_test.go` (behavior section)
   - Uses `testutil.Harness`, `testutil.FuzzDecoder.NextOp`.
   - Fixed options in deterministic tests: `KeySize=8`, `IndexSize=4`, `SlotCapacity=64` (ordered variant uses 256).

2) **Spec compliance / format correctness**
   - Files: `spec_fuzz_test.go`, `spec_fuzz_options_test.go`, `near_cap_fuzz_test.go` (spec section)
   - Uses `testutil.ValidateFile` (spec oracle) and custom `actionByte` switch (NOT `NextOp`).
   - Uses `FuzzDecoder` only for key/index/prefix bytes.
   - Recently extended to include `Invalidate()` and `SetUserHeader*` actions; resets cache after invalidation.

3) **Robustness / mutation**
   - Files: `robustness_fuzz_test.go`
   - Uses `MutateBytes` + `ByteStream`, no `FuzzDecoder`.
   - Purpose: Open/read corrupted files and validate error classification.

4) **Concurrency / generation / seqlock / registry / open validation**
   - Files: `seqlock_*`, `concurrency_test.go`, `registry_test.go`, `open_validation_test.go`, etc.
   - Purpose: targeted invariants, not fuzz-based.

### Current generator behavior (important distribution details)
- `testutil.FuzzDecoder.NextOp` picks operations based on whether writer is active.
  - No writer: ~20% BeginWrite, ~80% reads
  - Writer active: ~45% Put, 15% Delete, 15% Commit, 10% Close, 15% reads
- Key generation:
  - ~15% invalid key (nil/wrong length)
  - Reuse increases as more keys are seen
  - Ordered mode: mostly monotonic, non-monotonic ~6%
- Index generation: ~10% invalid length
- Scan options: ~10% invalid

### Problem to solve
- Fast deterministic tests are **behaviorally strong** but **narrow**:
  - Only one option profile
  - Low collisions/tombstone stress
  - Short writer sessions
  - Invalid inputs reduce deep state
- Seeds are brittle if decoder logic changes; only filter seeds have guard tests.

## Diagram (how pieces connect)

```
                           ┌──────────────────────────────┐
                           │ pkg/slotcache (prod API)     │
                           └──────────────┬───────────────┘
                                          │
        ┌──────────────────────────────────┼───────────────────────────────────┐
        │                                  │                                   │
Behavior tests                       Spec compliance tests              Robustness/mutation
behavior_*                           spec_fuzz_* / near_cap             robustness_fuzz_*
        │                                  │                                   │
        │ uses                             │ uses                              │ uses
        ▼                                  ▼                                   ▼
testutil.Harness (model vs real)      custom op loop                     MutateBytes+ByteStream
        │                                  │                                   │
        │ uses                             │ uses                              │ uses
        ▼                                  ▼                                   ▼
testutil/model                         testutil.FuzzDecoder               DeriveFuzzOptions
(behavior-only oracle)                 (keys/index/prefix only)           testutil.ValidateFile
```

## Proposed refactor (testutil-first)

### 1) Add a reusable **behavior runner**
**New file:** `pkg/slotcache/internal/testutil/behavior_runner.go`

Provide a single function to run model-vs-real tests with configurable op sources.

**Sketch API:**
```go
// In testutil

type BehaviorRunConfig struct {
    MaxOps               int
    LightCompareEveryN   int // cheap checks (Len vs Scan count, fwd/rev) 
    HeavyCompareEveryN   int // full CompareState
    CompareOnCommit      bool
    CompareOnCloseReopen bool
}

type OpSource interface {
    NextOp(h *Harness, seen [][]byte) Operation
}

func RunBehavior(tb testing.TB, opts slotcache.Options, src OpSource, cfg BehaviorRunConfig)
```

**Speed note:** heavy comparisons (full `CompareState`) should be gated to commits/close/reopen or every N ops. The copy/aliasing checks should move to a dedicated deterministic test or only run during heavy checks.

### 2) Make op generation configurable
Introduce a config layer to tune probabilities (invalid inputs, commit rate, delete rate, non-monotonic rate, etc.).

**New file:** `internal/testutil/opgen_config.go`
```go
// Example knobs
InvalidKeyRate           int // %
InvalidIndexRate         int
InvalidScanOptsRate      int
CommitRate               int
CloseRate                int
DeleteRate               int
NonMonotonicRate         int // ordered mode
CollisionModeRate        int
MinWriterOpsBeforeCommit int
PhaseEnabled             bool
ForceProgressEveryN      int // force valid Put/Commit if no progress
SmallScanLimitRate       int // bias toward small Limit values
```

Create `OpGenerator` that wraps `FuzzDecoder` but applies these weights. This keeps byte-driven determinism while allowing fast tests to force deeper states.

**Determinism requirement:** all branching and weighting must consume bytes from `FuzzDecoder` (no global randomness), so fuzz minimization and fixed seeds remain stable.

### 3) Add **phased generation** (optional)
Implement a simple phase strategy (Fill → Churn → Read) driven by `len(seen)` vs `SlotCapacity`. Enable via config.

**OrderedKeys note:** collision-mode keys must remain monotonic (or collision mode is disabled) to avoid ErrOutOfOrderInsert dominating ordered-mode runs.

**Speed note:** bias scan options toward small limits most of the time (with occasional full scans) to keep fast tests quick while still exercising scan logic.

### 4) Option profiles in testutil
**New file:** `internal/testutil/options_profiles.go`

Provide deterministic profiles (your five requested configs) so deterministic tests can loop them:
- `(KeySize=1, IndexSize=0, SlotCapacity=2)`
- `(KeySize=7, IndexSize=0, SlotCapacity=4)`
- `(KeySize=9, IndexSize=3, SlotCapacity=8)`
- `(KeySize=16, IndexSize=0, SlotCapacity=1)`
- `(KeySize=8, IndexSize=4, SlotCapacity=8, OrderedKeys=true)`

Profiles should be **templates** (no Path). Tests inject a per-run Path before opening files.

Also add a helper to pick profiles by seed byte if desired.

### 5) Move curated seeds + add guard helpers
Move curated seed bytes into testutil and add a helper to assert intended behavior:

**New file:** `internal/testutil/behavior_seeds.go`
```go
var SeedBehaviorFilteredScans []byte
var SeedBehaviorFilterPagination []byte
```

**New helper:**
```go
func AssertSeedEmitsFilteredScan(tb testing.TB, seed []byte, opts slotcache.Options, maxOps int)
```

Keep a thin wrapper test in `pkg/slotcache` so `go test ./pkg/slotcache` still runs guards.

### 6) Add more guard tests for hand-rolled seeds
Add guard tests for the hand-rolled seeds in `behavior_fuzz_test.go` (A–H), validating **milestones** rather than full op-by-op traces:
- "Seed A must include BeginWrite → Put → Commit → Get → Scan → ScanPrefix"
- "Seed B must update same key; final revision == 2"
- "Seed D must prove Writer.Close discards ops"
- "Seed E must exercise ErrBusy for Close/Reopen while writer active"
- etc.

**Keep guards minimal** (milestones only) to avoid brittleness if op generation evolves.

These can live in `internal/testutil` with a wrapper test under `pkg/slotcache`.

### 7) Add spec-fuzz seed improvements (small, targeted)
Add a few spec-fuzz seeds that explicitly hit:
- `Invalidate()` path
- `SetUserHeaderFlags` and `SetUserHeaderData`
- BeginWrite → Put → Commit

Keep seeds minimal and add a guard if they're hand-rolled.

## Detailed task breakdown

### Phase A — testutil infrastructure
- [x] 1. Add `behavior_runner.go` with `RunBehavior` and light/heavy compare scheduling.
- [x] 2. Add `opgen_config.go` + `OpGenerator` (wrapper around `FuzzDecoder`).
- [x] 3. Add optional phased generation logic (byte-driven, deterministic) with small-scan bias.
- [x] 4. Add `options_profiles.go` with deterministic profile templates (Path injected per run).
- [x] 5. Move `maxFuzzOperations` (or equivalent) into testutil so guards can reuse it.
- [x] 6. Add `behavior_seeds.go` with curated seeds.
- [x] 7. Add guard helpers in testutil (seed assertions + minimal trace inspection).
- [x] 8. Add a dedicated copy/aliasing regression test (or move copy checks into heavy-compare only).

### Phase B — update behavior tests
- [x] 1. Refactor deterministic tests (`behavior_deterministic_seed_test.go`) to use profiles + `RunBehavior`.
  - [x] Replace inline loop with `testutil.RunBehavior`.
  - [x] Iterate over option profiles from testutil.
  - [x] Use an `OpGenerator` config optimized for deeper state (lower invalid rate, longer writer sessions, mild collision mode).
  - [x] Configure light/heavy compare cadence (e.g., light every op, heavy on commit/close/reopen).
- [x] 2. Refactor behavior fuzz targets (`behavior_fuzz_test.go`) to use `RunBehavior`.
  - [x] Keep separate fuzz target (fixed config) for deep coverage.
  - [x] Use shared `RunBehavior` + `OpGenerator` config.
  - [x] Favor shorter heavy-compare cadence to keep fuzz throughput high.
- [x] 3. Refactor behavior fuzz options (`behavior_fuzz_options_test.go`), keep separate from fixed-config fuzz.
  - [x] Keep separate fuzz target (derived options) for edge configs.
  - [x] Use shared `RunBehavior` + `OpGenerator` config.
  - [x] Reduce per-iteration ops for heavier option profiles (large key/index sizes).

### Phase C — add regression guard tests
- [x] 1. Move/replace `behavior_filter_seed_guard_test.go` to use testutil helper.
  - [x] Move seeds to testutil.
  - [x] Use `testutil.AssertSeedEmitsFilteredScan`.
  - [x] Keep wrapper test in `pkg/slotcache` for slotcache-only test runs.
- [ ] 2. Add guard tests for hand-rolled behavior seeds (A–H).
- [ ] 3. Add guard tests for spec-fuzz seeds if new ones are added.

### Phase D — spec fuzz seed improvements
- [ ] 1. Add 2–3 targeted seeds for invalidation + user header updates (keep corpus small).
- [ ] 2. Use testutil seed builder helper to reduce brittleness.

## Risk notes / gotchas
- **Core seeds (A-H) need fixing:** Guard tests revealed that seeds in `behavior_seeds.go` (except filter seeds) have incorrect byte encoding. For example, they use `0x02` for Commit but the decoder needs choice in [60,75) range, so `0x3C` (60) is required. Filter seeds (`SeedFilteredScans`, `SeedFilterPagination`) are correctly encoded. Fix the core seeds before adding guard tests for them in Phase C.
- **Determinism:** Changing `FuzzDecoder` logic impacts seed semantics. Guard tests prevent silent drift.
- **Test visibility:** `internal/testutil` tests won't run with `go test ./pkg/slotcache`; keep wrapper tests in `pkg/slotcache` for guards.
- **Seed corpus size:** keep curated spec seeds minimal so fuzz startup stays fast.
- **Spec fuzz vs behavior fuzz:** Keep op generators separate; spec fuzz must keep its custom `Invalidate()` reset logic.
- **Invalidation:** behavior harness intentionally excludes invalidation (per spec + implementation plan).
- **CompareState cost:** keep copy/aliasing checks out of the hot loop; use a dedicated regression test or heavy-only scheduling.

## Suggested follow-up metrics
- Count of seeds that trigger collision-mode operations.
- Frequency of ErrFull and ErrOutOfOrderInsert in deterministic runs.
- Number of filtered scans per deterministic seed.
