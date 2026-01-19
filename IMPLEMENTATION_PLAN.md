# slotcache Implementation Plan (SLC1)

## Current State (2026-01-19)

- `pkg/slotcache/slotcache.go` + `pkg/slotcache/writer.go` implement the **real SLC1 mmap-backed format**.
- `make test` **passes** - all behavioral tests, model parity, and spec-oracle validation pass.
- `make lint` **passes** - all style and safety checks pass.

**Important:** There are still spec-conformance gaps that are not reliably caught by the current test suite:

- **Corruption vs overlap classification:** some read-path invariant failures should be treated as overlap (retry/ErrBusy) unless generation is proven stable and unchanged.

---

## Completed Work ✅

### P0 — Core SLC1 Implementation (DONE 2026-01-18)

1. **OrderedKeys header flag** ✅ - Added flag constant, updated `newHeader()`.
2. **SLC1 file abstraction (fd + mmap)** ✅ - Using `syscall.Mmap(MAP_SHARED)`, per-file `sync.RWMutex` registry.
3. **Open() per spec** ✅ - Full validation, all file states handled, odd generation detection.
4. **In-process single-writer (dev+inode)** ✅ - Package-global registry keyed by `(dev, ino)`.
5. **Bucket index (hash table)** ✅ - FNV-1a 64-bit, linear probing, EMPTY/TOMBSTONE/FULL encoding.
6. **Writer commits to SLC1** ✅ - Buffered ops, effective delta, seqlock publish, CRC recompute.
7. **Cache read paths** ✅ - All scan variants work with seqlock retry.
8. **Close semantics** ✅ - Idempotent, proper cleanup.
9. **Safe integer conversions** ✅ - Added helper functions to avoid gosec G115 warnings.
10. **Function ordering** ✅ - Reorganized cache and writer methods per linter requirements.

---

## Prioritized Work (TODO)

### P0 — Spec conformance hardening (seqlock + Open correctness)

- [x] **Make `generation` atomic across processes** ✅ (2026-01-19)
  - Added `atomicLoadUint64()` and `atomicStoreUint64()` helpers in format.go using `sync/atomic` + `unsafe`.
  - Updated `readGeneration()` in slotcache.go to use atomic load.
  - Updated `Writer.Commit()` in writer.go to use atomic store for both odd and even generation publishes.
  - Go atomics provide seq-cst ordering which satisfies the spec's acquire/release requirement.

- [x] **Fix `Open()` behavior under concurrent commits** ✅ (2026-01-19)
  - Added `handleCRCFailure()` helper that re-reads generation when CRC validation fails.
  - If generation changed or is now odd, returns `ErrBusy` instead of `ErrCorrupt`.
  - When locking enabled: tries to acquire lock to distinguish active writer vs crashed writer.
  - Removed dead code (redundant second odd-generation check that was unreachable).

- [x] **Treat "impossible invariants" as overlap unless generation is stable** ✅ (2026-01-19)
  - Added `checkInvariantViolation()` helper that re-reads generation when impossible invariants are detected.
  - Updated `lookupKey()` to accept expected generation parameter and call helper for:
    - bucket→tombstoned slot
    - slot_id out of range (beyond highwater)
    - probed all buckets without finding EMPTY
  - If generation changed/odd → returns internal `errOverlap` → caller retries
  - If same even generation → returns `ErrCorrupt` (real corruption)

- [x] **Make slot `meta` and `revision` atomic (spec strictness)** ✅ (2026-01-19)
  - Added `atomicLoadInt64()` and `atomicStoreInt64()` helpers in format.go using `sync/atomic` + `unsafe`.
  - Updated all mmap-based slot `meta` reads/writes to use `atomicLoadUint64()`/`atomicStoreUint64()`.
  - Updated all mmap-based slot `revision` reads/writes to use `atomicLoadInt64()`/`atomicStoreInt64()`.
  - Note: `index` remains non-atomic and is protected by seqlock stability.

- [x] **Regression tests** ✅ (2026-01-19)
  - Added `seqlock_overlap_deterministic_test.go` with 9 deterministic tests covering:
    - `checkInvariantViolation()` correctly distinguishes overlap (generation changed/odd) from corruption
    - Bucket→tombstoned slot detection: returns `ErrBusy` when generation odd, `ErrCorrupt` when stable
    - Slot beyond highwater detection: returns `ErrBusy` when generation odd, `ErrCorrupt` when stable
    - Read operations (`Get`, `Len`, `Scan`) return `ErrBusy` when generation is odd
    - Generation counter properly increments and remains even after commits
  - These tests inject specific file states to deterministically verify overlap vs corruption classification,
    complementing the stress-style tests in `seqlock_concurrency_test.go` and `seqlock_torn_bytes_test.go`.

---

### P1 — Spec completeness (durability + crash semantics)

- [x] Implement `WritebackMode` ✅ (2026-01-18):
  - `WritebackNone`: no `msync`
  - `WritebackSync`: `msync` barriers per spec (header odd → data → header even)
  - ensure `msync` ranges are page-aligned (macOS requirement)
  - if any `msync` fails: still complete commit and return `ErrWriteback`
- [x] Implement tombstone-driven rehashing (e.g. when `bucket_tombstones/bucket_count > 0.25`) during Commit. ✅ (2026-01-19)
  - Added `rehashThreshold` constant (0.25) in writer.go.
  - Added `rehashBuckets()` method that clears all buckets and re-inserts entries for live slots.
  - Called during Commit after all operations are applied, before CRC recomputation.
  - Resets `bucket_tombstones` to 0 after rehash.
  - Added behavioral tests in `rehash_test.go` verifying correctness through public API.
  - **Note:** Benefit is limited without resizing - only reduces probe chain length for lookups.
    Slot tombstones remain (append-only). For severe fragmentation, rebuild from source of truth.
- [x] Implement bounded point-read retries with backoff; return `ErrBusy` after exhausting retries. ✅ (2026-01-19)
  - Added `readMaxRetries=10`, `readInitialBackoff=50µs`, `readMaxBackoff=1ms` constants.
  - Exponential backoff schedule: 0, 50µs, 100µs, 200µs, 400µs, 800µs, 1ms (capped).
  - Total worst-case delay ~6.55ms before returning `ErrBusy`.
  - Documented in `pkg/slotcache/specs/TECHNICAL_DECISIONS.md` §8.

---

### P2 — Performance and hardening

- [ ] Fix fd sentinel handling
  - `cache.fd` currently uses `0` as the "closed" sentinel, but fd 0 is valid.
  - Use `-1` as sentinel or store `*os.File` instead.

- [ ] Ordered range scan optimization: binary search to find start slot, then sequential scan.
- [ ] Optional extra corruption detection:
  - sample-check buckets at Open
  - stricter invariant checks during reads (return `ErrCorrupt` under stable even generation)
- [ ] Allocation reductions in scans (without violating "returned slices are caller-owned").

---

## Validation / Done Criteria

- [x] `make test` passes (includes the fuzz regression `FuzzSpec_GenerativeUsage/*`).
- [x] `make lint` passes.
- [x] `go test ./pkg/slotcache -fuzz=FuzzSpec_GenerativeUsage -fuzztime=30s` runs without failures. ✅ (2026-01-18)
- [x] `go test ./pkg/slotcache -fuzz=FuzzSpec_OpenAndReadRobustness -fuzztime=30s` runs without panics/hangs. ✅ (2026-01-18)
- [x] `go test ./pkg/slotcache -fuzz=FuzzBehavior_ModelVsReal -fuzztime=30s` runs without failures. ✅ (2026-01-18)
- [x] `go test ./pkg/slotcache -fuzz=FuzzBehavior_ModelVsReal_OrderedKeys -fuzztime=30s` runs without failures. ✅ (2026-01-18)

Additional recommended checks (spec hardening):
- [ ] `go test ./pkg/slotcache -run Seqlock -slotcache.concurrency-stress=5s` passes reliably.

---

### P3 — Cleanup

- [ ] Replace `writer_lock.go` with `pkg/fs.Locker` - use it directly with a real FS inside `slotcache.go`.
- [ ] Wrap sentinel errors with helpful context in `slotcache.go` and `writer.go`. Use `fmt.Errorf("useful context: %w", ErrFoo)` - include relevant data (keys, sizes, counts, etc.) as appropriate per call site. `errors.Is` still works but users get actionable diagnostics. Sentinels: `ErrCorrupt`, `ErrIncompatible`, `ErrBusy`, `ErrFull`, `ErrClosed`, `ErrWriteback`, `ErrInvalidInput`, `ErrUnordered`, `ErrOutOfOrderInsert`.
