# slotcache technical decisions (Go implementation)

This document records *stable* technical decisions for the Go implementation of **slotcache** in this repository.

Framing:

- The **normative** specification for behavior and the on-disk **SLC1** format is in this directory (`./001-overview.md`, `./002-format.md`, `./003-semantics.md`).
- The public Go API contract (types, method contracts, error taxonomy) is defined in `pkg/slotcache/api.go`.
- **Nothing in this document may contradict the spec or the public API.**
- Where the spec explicitly leaves details **implementation-defined** (e.g. retry/backoff parameters, sizing heuristics, internal structure), we record our decisions here so the implementation stays consistent over time.
- `IMPLEMENTATION_PLAN.md` is intentionally ephemeral; this file is intended to remain stable across plan rewrites.

Last updated: 2026-01-18

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

- Maintain a per-file (dev+inode keyed) in-process `sync.RWMutex`.
  - Readers take `RLock` only while reading/copying from the mmap.
  - `Writer.Commit()` takes `Lock` during the publish window (`generation` odd → mutations → `generation` even).
- Cursor iteration must not hold locks while calling the user-provided `yield` function.
  - Lock only for the brief “copy one entry” step.

---

## 4) In-process writer registry (dev+inode)

Because `flock()` is per-process, it does **not** prevent multiple writers inside the same process.

Decision:

- Enforce “at most one active Writer per file per process” using a package-global registry keyed by `(device, inode)`.
- `BeginWrite()` must consult this registry so concurrent goroutines / multiple Cache instances cannot acquire more than one writer for the same file.

---

## 5) Defaults (safe, spec-aligned)

- Bucket sizing: `bucket_count = nextPow2(slot_capacity * 2)` (load factor ≤ 0.5).
- Rehash threshold: trigger rebuild when `bucket_tombstones / bucket_count > 0.25` (during commit).
- Read retry/backoff: bounded exponential backoff; must eventually return `ErrBusy` (never infinite spin).

---

## 6) Writer thread-safety

- `BeginWrite()` is safe to call concurrently.
- Once acquired, a `Writer` is **not** goroutine-safe; callers must synchronize access.

(Aligned with `pkg/slotcache/specs/003-semantics.md`.)
