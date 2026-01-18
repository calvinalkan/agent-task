# slotcache Implementation Plan (SLC1)

## Current State (2026-01-18)

- `pkg/slotcache/slotcache.go` + `pkg/slotcache/writer.go` implement the **real SLC1 mmap-backed format**.
- `make test` **passes** - all behavioral tests, model parity, and spec-oracle validation pass.
- `make lint` **passes** - all style and safety checks pass.

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

### P1 — Spec completeness (durability + crash semantics)

- [x] Implement `WritebackMode` ✅ (2026-01-18):
  - `WritebackNone`: no `msync`
  - `WritebackSync`: `msync` barriers per spec (header odd → data → header even)
  - ensure `msync` ranges are page-aligned (macOS requirement)
  - if any `msync` fails: still complete commit and return `ErrWriteback`
- [ ] Implement tombstone-driven rehashing (e.g. when `bucket_tombstones/bucket_count > 0.25`) during Commit.
- [ ] Implement bounded point-read retries with backoff; return `ErrBusy` after exhausting retries.

---

### P2 — Performance and hardening

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
