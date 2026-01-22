# slotcache technical decisions (Go implementation)

This document records *stable* technical decisions for the Go implementation of **slotcache** in this repository.

Framing:

- The **normative** specification for behavior and the on-disk **SLC1** format is in this directory (`./001-overview.md`, `./002-format.md`, `./003-semantics.md`).
- The public Go API contract (types, method contracts, error taxonomy) is defined in `pkg/slotcache/api.go`.
- **Nothing in this document may contradict the spec or the public API.**
- Where the spec explicitly leaves details **implementation-defined** (e.g. retry/backoff parameters, sizing heuristics, internal structure), we record our decisions here so the implementation stays consistent over time.
- `IMPLEMENTATION_PLAN.md` is intentionally ephemeral; this file is intended to remain stable across plan rewrites.

Last updated: 2026-01-19

---

## 1) Cache.Close() is idempotent

- `Cache.Close()` MUST be safe to `defer` and MUST be idempotent.
- Calling `Close()` on an already-closed cache returns **nil**.
- After a successful `Close()`, all other cache methods return `ErrClosed`.
- If a writer is active, `Close()` returns `ErrBusy`.

Rationale: aligns with `pkg/slotcache/api.go` and common Go patterns.

---

## 2) Open() locking strategy (don’t always take the writer lock)

Goal: avoid corrupting the file under concurrent access, without turning `Open()` into a lock-contention point.

- `Open()` does **not** always acquire the writer lock.
- When opening an existing non-empty file:
  - If `generation` is **even**: proceed with validation + mmap.
  - If `generation` is **odd**:
    - With locking enabled: attempt to acquire the writer lock **non-blocking**.
      - If lock is acquired: no writer is active → treat as a crashed writer → return `ErrCorrupt`.
      - If lock is busy: writer is active → return `ErrBusy`.
    - With locking disabled: return `ErrBusy` (caller must coordinate externally).
- When creating a new file or initializing a 0-byte file and locking is enabled, `Open()` SHOULD acquire the writer lock to serialize initialization.

---

## 3) In-process concurrency guard (avoid Go data races)

The cross-process correctness mechanism is the on-disk seqlock (`generation`).

However, inside a single Go process, a reader goroutine can overlap with a committing writer goroutine and touch the same mmapped memory concurrently. Even if the reader later retries due to generation change, that overlap is still an in-process data race in Go.

Decision:

- Maintain a per-file (dev+inode keyed) in-process `sync.RWMutex` (`fileRegistryEntry.mu`).
  - Read operations take `mu.RLock()` while touching the mmap (including calling `ScanOptions.Filter`, which receives borrowed slices).
  - `Writer.Commit()` takes `mu.Lock()` during the publish window (`generation` odd → mutations → `generation` even).
- Snapshot scans materialize results under `mu.RLock()` and return an in-memory `[]Entry` slice.
  - The returned slice is already fully detached from the mmap.

---

## 4) In-process writer registry (dev+inode)

Because `flock()` is per-process, it does **not** prevent multiple writers inside the same process.

Decision:

- Enforce “at most one active Writer per file per process” using a package-global registry keyed by `(device, inode)`.
- `Writer()` must consult this registry so concurrent goroutines / multiple Cache instances cannot acquire more than one writer for the same file.

---

## 5) Defaults (safe, spec-aligned)

- Bucket sizing: `bucket_count = nextPow2(slot_capacity * 2)` (load factor ≤ 0.5).
- Rehash threshold: trigger rebuild when `bucket_tombstones / bucket_count > 0.25` (during commit).
  - **Note:** The benefit of rehashing is limited since slotcache doesn't resize. Rehashing only eliminates bucket tombstones to reduce probe chain length during lookups. Slot tombstones remain (append-only design), and file size is unchanged. For severe fragmentation, rebuilding the entire cache from source of truth is the recommended approach.
- Read retry/backoff: bounded exponential backoff; must eventually return `ErrBusy` (never infinite spin).

---

## 6) Writer thread-safety

- `Writer()` is safe to call concurrently.
- Once acquired, a `Writer` is **not** goroutine-safe; callers must synchronize access.

(Aligned with `pkg/slotcache/specs/003-semantics.md`.)

---

## 7) Scan consistency: snapshot mode (no streaming scans)

The spec allows two scan strategies (“snapshot mode” and “streaming mode”); see **003-semantics.md → Scan consistency modes**.

Decision:

- All scan-style APIs (`Scan`, `ScanPrefix`, `ScanMatch`, `ScanRange`) use **snapshot mode**.
- We collect and copy all matching entries under a stable even `generation` before returning.
- If a stable generation cannot be acquired after bounded retries, the scan yields **no entries** and reports `ErrBusy`.
- We do **not** implement streaming scans that may yield partial results and then fail with `ErrBusy`.

Note: the public API returns an in-memory `[]Entry` slice for scans; it does not read from the mmap after returning.

---

## 8) Read retry/backoff parameters

The spec requires bounded retries with backoff for read operations (see **003-semantics.md → Reader coherence rule**). The specific parameters are implementation-defined.

**Parameters:**

| Parameter | Value | Rationale |
|-----------|-------|-----------|
| `readMaxRetries` | 10 | Balances responsiveness vs. giving the writer time to complete. 10 attempts is enough for most short commits. |
| `readInitialBackoff` | 50µs | Small enough for fast retry under brief contention, large enough to avoid pure busy-spinning. |
| `readMaxBackoff` | 1ms | Caps exponential growth; prevents excessive delays if retries are exhausted. |

**Schedule:**

- Attempt 0: immediate (no delay)
- Attempt 1: 50µs
- Attempt 2: 100µs
- Attempt 3: 200µs
- Attempt 4: 400µs
- Attempt 5: 800µs
- Attempt 6–9: 1ms (capped)

**Total worst-case delay:** ~5.55ms (sum of all backoffs if all 10 attempts fail).

**Why exponential backoff:** Starts fast to handle brief contention (writer finishing), then backs off to reduce CPU pressure during sustained contention (long commit). This balances latency for the common case (writer finishes quickly) against efficiency for the rare case (long commit or many retries).

**Why not longer timeouts:** slotcache is a "throwaway cache" — if the writer is holding the lock for extended periods (e.g., >10ms), callers should either:
1. Retry at the application level with longer intervals, or
2. Accept `ErrBusy` and fall back to the source of truth.

Callers needing guaranteed reads under heavy write load should use external coordination or queue writes.
