# Lifecycle

This document describes opening, closing, and crash recovery for an mddb database.

## Concurrency model

mddb uses a **single-writer, multi-reader** model:

- **Writers** (transactions, rebuilds, recovery) acquire an **exclusive** lock on `<data-dir>/.mddb/wal` using `flock(2)`.
- **Read transactions** acquire a **shared** lock on `<data-dir>/.mddb/wal`.
- **Lock-free reads** (`Get`, `Query`) do not acquire any lock in the common case.

`.mddb/wal` is both the write-ahead log file and the lock target. Because `flock` locks an inode, `.mddb/wal` MUST be treated as a stable lock file and MUST NOT be replaced (renamed over, unlinked, delete+recreate) while the database may be in use. Implementations MUST only modify `.mddb/wal` in place (for example using `ftruncate` and writes).

| Operation | Lock required | See |
|-----------|---------------|-----|
| `Open()` | None (fast path); exclusive lock if recovery/rebuild needed | Below |
| `Get(id)` | None (common case); exclusive lock if recovery needed | [Transactions](008-transactions.md#get-and-query-behavior) |
| `Query(...)` | None (common case); exclusive lock if recovery needed | [Transactions](008-transactions.md#get-and-query-behavior) |
| `BeginReadTx()` | Shared lock (may briefly take exclusive lock to ensure clean state) | [Isolation](#read-transactions) |
| `Begin()` | Exclusive lock (waits for read txs) | [Transactions](008-transactions.md) |
| `Commit()` | Exclusive lock | [Transactions](008-transactions.md) |

### Cache reads during writes

The slotcache uses a **seqlock** (generation counter) for lock-free reads:

- **Even generation** = stable, safe to read
- **Odd generation** = write in progress

Readers retry automatically (handled by slotcache) if they observe an odd generation or if the generation changes mid-read. In practice, the odd window is tiny (just the mmap mutations), so contention is rare.

If the cache file is being replaced by another process, `slotcache` returns `ErrInvalidated`. In that case, mddb MUST close/unmap and re-open the cache and retry the operation (bounded).

See [Cache](010-cache.md) for slotcache integration details.

### Visibility guarantees

mddb uses the reserved per-entry cache byte `in_wal` as a hard barrier.

- `Query(...)` treats `in_wal` as a barrier while scanning: before evaluating any predicate on a visited entry, it checks `in_wal`. If any visited entry has `in_wal = 1`, it triggers recovery and retries.
- `Get(id)` checks the cache entry's `in_wal` before reading the file. If `in_wal = 1`, it triggers recovery and retries.
- To avoid a time-of-check/time-of-use race, `Get(id)` MUST also re-check `in_wal` after reading the file and MUST NOT return the document if it observes `in_wal = 1` on that second check.
- Document writes are atomic (temp + rename), so readers never see partial files.

If recovery is required, reads acquire the exclusive WAL lock, replay/truncate the WAL as needed, rebuild the cache if required, then retry. This ensures lock-free readers never return a view that is inconsistent with a committed WAL.

See [Transactions](008-transactions.md#get-and-query-behavior) for the full protocol.

## Isolation and snapshots

### Default behavior (no read transaction)

Each `Query()` returns a **consistent snapshot** of the cache at some point in time.

Internally, slotcache uses a seqlock to ensure snapshot consistency:

1. Read generation (must be even, else spin)
2. Scan slots
3. Read generation again
4. If generation changed, retry from step 1
5. After max retries, return `ErrBusy`

This means:

- A successful `Query()` always returns consistent data (no partial writes visible)
- Under heavy write contention, `Query()` may return `ErrBusy` if a stable snapshot cannot be acquired within the retry limit
- Consecutive `Query()` calls may see different committed states

Unlike MVCC databases (e.g., PostgreSQL), mddb does not keep old versions of data. This means reads can fail under write contention rather than returning an older snapshot.

For long-running scans or multiple reads that must see consistent state, use a read transaction.

### Read transactions

`BeginReadTx()` acquires a **shared lock** on `<data-dir>/.mddb/wal`, providing stronger guarantees:

- Writers (`Begin()`) block until all read transactions close
- Queries within a read transaction do not fail due to write contention (writers are blocked)
- Multiple queries see the same committed state (**Repeatable Read**)

#### BeginReadTx

```
BeginReadTx() → ReadTx, error
BeginReadTx(timeout) → ReadTx, error
```

Acquires a shared lock on `<data-dir>/.mddb/wal`.

**Clean-state requirement:** `BeginReadTx()` MUST NOT return a read transaction while the WAL is non-empty. If the WAL is non-empty, `BeginReadTx()` MUST first ensure recovery has completed (by briefly acquiring the exclusive WAL lock and running recovery) and only then acquire and return the shared-lock read transaction.

**Cache stability (recommended):** `BeginReadTx()` SHOULD ensure the cache is usable and can yield a stable snapshot before returning. If cache access fails with `ErrBusy` (odd/stuck generation), `ErrCorrupt`, `ErrIncompatible`, or the cache is missing, `BeginReadTx()` SHOULD release its shared lock, acquire the exclusive WAL lock, rebuild/refresh the cache (and/or replay/truncate the WAL as needed), then retry.

Multiple read transactions may be active concurrently. A write transaction (`Begin()`) waits for all active read transactions to close before acquiring its exclusive lock.

#### ReadTx.Query / ReadTx.Get

Same semantics as `Query()` / `Get()`, but:

- Multiple calls see the same snapshot
- Writers are blocked, so these operations do not fail due to write contention

#### ReadTx.Close

Releases the shared lock. MUST be called when done.

After `Close()`, the read transaction handle MUST NOT be used.

#### When to use read transactions

| Scenario | Recommendation |
|----------|----------------|
| Quick single query | `Query()` directly (no lock overhead) |
| Long-running scan | `BeginReadTx()` to avoid `ErrBusy` |
| Multiple related queries | `BeginReadTx()` for consistent view |
| Query + Get combinations | `BeginReadTx()` so both see same state |
| Bulk export | `BeginReadTx()` for stable snapshot |

## Open

Opening a database requires configuration and performs initialization.

### Configuration

#### Required

| Option | Description | See |
|--------|-------------|-----|
| Data directory | Root path for documents and `.mddb/` | [Filesystem](002-filesystem.md) |
| IDSpec | Key size, validation, encoding | [IDs](004-id.md) |
| Index schema | Field definitions, produces `IndexSize` | [Index schema](006-index-schema.md) |
| PathOf | Maps `id` → relative path | [Path mapping](005-path-mapping.md) |

#### Optional

| Option | Description | See |
|--------|-------------|-----|
| IdFromPath | Inverse of PathOf (optimization) | [Path mapping](005-path-mapping.md) |
| LayoutID | Stable identifier for PathOf behavior (recommended) | [Path mapping](005-path-mapping.md) |
| SyncMode | Durability level (default: SyncNone) | [Transactions](008-transactions.md) |
| Ordered-keys | Maintain lexicographic slot order | [Cache](010-cache.md) |
| Rebuild strictness | Strict (default) or best-effort | [Rebuild](011-rebuild.md) |

### Open behavior

`Open()` MUST:

1. Ensure `<data-dir>/.mddb/` exists (create if needed)
2. Attempt to open the slotcache index **without** acquiring the WAL lock
   - If `slotcache` returns `ErrInvalidated`, `Open()` SHOULD retry opening the cache (bounded)
   - If `slotcache` returns `ErrBusy` (unable to acquire a stable snapshot), `Open()` SHOULD proceed to the slow path (acquire the exclusive WAL lock)
3. `stat(<data-dir>/.mddb/wal)` (treat a missing WAL file as empty)
4. If the cache is valid (exists, compatible, stable snapshot) and the WAL is empty (`size == 0`): return the database handle (**fast path**)
5. Acquire the **exclusive** WAL lock
6. Under the lock:
   - If the WAL is non-empty: run the WAL recovery protocol (see [Transactions](008-transactions.md#wal-replay-protocol))
   - If the cache is missing/corrupt/incompatible/invalidated, cannot yield a stable snapshot, or is marked for rebuild: rebuild from documents
7. Release the exclusive WAL lock
8. Return the database handle

**Critical ordering:** `Open()` MUST NOT return a handle that could serve `Query()` results from a cache snapshot that predates a successfully replayed WAL.

In the common case (valid cache, empty WAL), `Open()` does not acquire the WAL lock. The lock is only needed for recovery or cache rebuild.

After open, the handle is read-only. To write, call `Begin()` which acquires the exclusive WAL lock (see [Transactions](008-transactions.md)).

### Compatibility checks

On open, mddb MUST verify cache compatibility by checking that the stored `UserVersion` matches a hash of:

- IDSpec (KeySize, encoding)
- Index schema (field definitions)
- LayoutID
- Ordered-keys mode

If any mismatch, the cache MUST be rebuilt.

## Close

`Close()` releases resources and ensures clean shutdown.

`Close()` MUST:

1. If a write transaction is active, abort it
2. Unmap/close the slotcache file
3. Release any file handles

After `Close()`, the database handle MUST NOT be used.

### Concurrent readers

Multiple processes MAY have the database open simultaneously for reading. Lock-free reads do not hold the WAL lock, so they are unaffected by `Close()` in another process.

Only a process holding the WAL lock (during a transaction or recovery) is affected by lock release.

## Crash recovery

If the process crashes or is killed without calling `Close()`:

1. OS releases file locks automatically
2. `.mddb/wal` may remain non-empty (committed or uncommitted)
3. Cache may be in inconsistent state (odd generation = in-progress write)
4. Cache may be invalidated (if a cache swap was initiated)
5. Some documents may have `in_wal = 1` in the cache

### Recovery via Open()

On next `Open()`, under the exclusive WAL lock:

1. If `.mddb/wal` is non-empty, run the WAL replay protocol (see [Transactions](008-transactions.md#wal-replay-protocol)).
2. If the cache is missing/corrupt/incompatible/invalidated or left at odd generation, rebuild it from documents.

If WAL replay fails with `ErrWALCorrupt`, `Open()` MUST return an error. Operators MAY choose to perform a force recovery that discards the WAL after inspection; see [Transactions](008-transactions.md#corrupt-wal-force-recovery).

### Recovery via Get()/Query()

Long-lived processes MAY recover without reopening.

A read MUST trigger recovery and retry if it encounters `in_wal = 1` for any document.

A read SHOULD also attempt recovery and retry if any of the following are true:

- Cache access returns `ErrInvalidated` (cache file replaced)
- Cache access repeatedly returns `ErrBusy` (unable to acquire a stable snapshot)
- Cache is missing, corrupt, or incompatible

Implementations that do not attempt recovery in these cases MUST return a classified error.

This ensures that even long-lived processes that don't re-open the database will still recover from crashes.

mddb guarantees **roll-forward correctness**: if a committed WAL exists, its operations will be applied. There is no rollback.

### Durability after crash

Whether committed data survives depends on `SyncMode`:

- `SyncNone`: data may be lost on power failure (but not process crash)
- `SyncData`: WAL and documents are fsync'd
- `SyncAll`: directories are fsync'd as needed for rename/unlink durability

See [Transactions](008-transactions.md) for details.
