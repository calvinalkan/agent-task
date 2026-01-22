# slotcache Testing

## Philosophy

We favor **property-based testing** over handwritten unit tests. The core correctness guarantee comes from comparing the real implementation against an [in-memory reference model](internal/testutil/model/model.go) across thousands of randomized operation sequences.

## Test Categories

### 1. Property-Based Tests (Primary)

These are the main correctness tests. They run thousands of operation sequences and verify invariants hold across all of them.

| File | What it verifies |
|------|------------------|
| `slotcache_test.go` | **Model comparison** — every API call returns the same result as the reference model and state is the same after every operation |
| `slotcache_metamorphic_test.go` | **Semantic invariants** — last-write-wins, Get/Scan agreement, pagination correctness, persistence across reopen |

If these pass, the implementation is almost certainly correct. Most bugs should be caught here automatically without writing new tests.

### 2. Invariant-Specific Tests

These test **specific error conditions** and edge cases that are easier to verify with targeted tests than through random generation. Grouped by the error/behavior they verify:

| File | Invariant |
|------|-----------|
| `corruption_test.go` | `ErrCorrupt` is returned when file state is invalid (bad bucket refs, reserved bits set, etc.) |
| `concurrency_test.go` | `ErrBusy` is returned during concurrent access (writer locks, seqlock protocol, cross-process) |
| `invalidation_test.go` | `ErrInvalidated` is returned after cache invalidation; user header semantics |
| `validation_test.go` | `ErrInvalidInput` / `ErrIncompatible` for bad options or incompatible reopen |
| `writer_test.go` | Writer correctness: rehash after deletions, copy semantics (returned entries are detached), generation counter |
| `scan_test.go` | Scan variants: pagination, ordering validation, filter application, range bounds |
| `format_test.go` | Low-level format helpers: slot encoding, CRC, bucket count computation |

### 3. Fuzz Tests

Continuous fuzzing for deeper exploration:

| File | Target |
|------|--------|
| `slotcache_fuzz_test.go` | Random operation sequences |
| `slotcache_format_fuzz_test.go` | Format roundtrips |
| `slotcache_mutation_fuzz_test.go` | Corruption detection |

## Test Infrastructure

The property-based test infrastructure lives in `internal/testutil/`. See [`internal/testutil/README.md`](internal/testutil/README.md) for details on:

- The model-vs-real harness
- Operation generation from byte streams
- Curated seed construction
- The spec oracle for file format validation

## Adding New Tests

1. **Found a bug?** First check if adding a curated seed to the property tests catches it
2. **New error condition?** Add to the invariant-specific test file for that error type
3. **New semantic invariant?** Add to `slotcache_metamorphic_test.go`
4. **Format change?** Update `format_test.go`

Most behavioral bugs should be caught by the property-based tests. Only add invariant-specific tests when:
- The condition is hard to hit through random generation
- You want to document a specific edge case
- Cross-process behavior needs explicit testing
