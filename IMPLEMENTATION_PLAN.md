# slotcache Implementation Plan (SLC1)

## Current State (2026-01-18)

- `pkg/slotcache/slotcache.go` + `pkg/slotcache/writer.go` implement the **real SLC1 mmap-backed format**.
- `make test` **passes** - all behavioral tests, model parity, and spec-oracle validation pass.
- `make lint` **passes** - all style and safety checks pass.

**Important:** There are still spec-conformance gaps that are not reliably caught by the current test suite:

- **Seqlock atomicity:** `generation` is currently read/written via `binary.LittleEndian` helpers, not atomic 64-bit ops.
  - Spec requires cross-process atomic 64-bit load/store with acquire/release ordering.
- **Open() vs concurrent commit:** `Open()` can misclassify in-progress commit windows.
  - Example: observing a CRC mismatch while a writer is active should be `ErrBusy`, not `ErrCorrupt`.
  - The "odd generation + can acquire lock" path should re-read generation while holding the lock to avoid false positives.
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

- [ ] **Make `generation` atomic across processes**
  - Replace `binary.LittleEndian` generation loads/stores with `sync/atomic` + `unsafe` on an 8-byte-aligned `*uint64`.
  - Use acquire/release (Go atomics are seq-cst, which is fine).
  - Ensure both readers and writer commit publish use the atomic helpers.

- [ ] **Fix `Open()` behavior under concurrent commits**
  - Avoid returning `ErrCorrupt` due to transient header CRC mismatch while a writer is active.
  - When generation is odd and locking is enabled:
    - if lock is busy → `ErrBusy`
    - if lock is acquired → re-read generation while holding lock; only treat as crashed writer (`ErrCorrupt`) if it is still odd.

- [ ] **Treat "impossible invariants" as overlap unless generation is stable**
  - For cases like bucket→tombstoned slot, slot_id out of range, etc:
    - re-read generation; if changed/odd → overlap → retry/ErrBusy
    - if same even generation → real corruption → `ErrCorrupt`

- [ ] **Make slot `meta` and `revision` atomic (spec strictness)**
  - Replace bytewise reads/writes of slot `meta` (u64) and `revision` (i64) with atomic 64-bit ops (`sync/atomic` + `unsafe`).
  - Stop writing these fields via bytewise helpers (e.g. `binary.LittleEndian.PutUint64`, `putInt64LE`), because those are not atomic.
  - Note: `index` remains non-atomic and is protected by seqlock stability.

- [ ] **Regression tests**
  - Keep/extend the deterministic seqlock overlap tests so that incorrect publication/orderings are caught.

---

### P1 — Spec completeness (durability + crash semantics)

- [x] Implement `WritebackMode` ✅ (2026-01-18):
  - `WritebackNone`: no `msync`
  - `WritebackSync`: `msync` barriers per spec (header odd → data → header even)
  - ensure `msync` ranges are page-aligned (macOS requirement)
  - if any `msync` fails: still complete commit and return `ErrWriteback`
- [ ] Implement tombstone-driven rehashing (e.g. when `bucket_tombstones/bucket_count > 0.25`) during Commit.
- [ ] Implement bounded point-read retries with backoff; return `ErrBusy` after exhausting retries.
  - Add explicit parameters (attempt count + backoff schedule) and document them in `pkg/slotcache/specs/TECHNICAL_DECISIONS.md`.

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
