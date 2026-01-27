# Transactions

mddb supports **single-writer**, **multi-document** transactions with roll-forward recovery.

- Multiple processes MAY read concurrently.
- At most one process MAY hold the write lock and commit at a time.

Transactions are write-only: they buffer operations in memory until `Commit()`. Reads (`Get`, `Query`) always see committed state.

## Locking

mddb coordinates access using advisory file locks (`flock(2)`) on:

- `<data-dir>/.mddb/wal`

Lock modes:

- **Exclusive lock**: writers (`Begin()`/`Commit()`), recovery, rebuild, cache swap
- **Shared lock**: read transactions (`BeginReadTx()`)

Because `flock` locks an inode, `.mddb/wal` MUST be treated as a stable lock file and MUST NOT be replaced (renamed over, unlinked, delete+recreate) while the database may be in use. Implementations MUST only modify `.mddb/wal` in place (for example using `ftruncate` and writes).

## Begin

```
Begin() → Tx, error
Begin(timeout) → Tx, error
```

`Begin()` starts a write transaction:

1. Acquires the **exclusive** lock on `.mddb/wal` (waits for any active read transactions to close)
2. If the WAL is non-empty, runs recovery first (see [WAL replay protocol](#wal-replay-protocol))
3. Creates an empty operation buffer
4. Returns the transaction handle

A process MUST NOT have more than one active write transaction at a time (per data directory).

## Operations

Transactions support three operations:

- `Create(id, doc)` — create a new document
- `Update(id, patch)` — modify an existing document
- `Delete(id)` — remove a document

All operations perform **existence checks at call time**. Since the exclusive WAL lock is held, no other mddb process can modify files between the check and commit.

External edits (outside mddb) can still occur and may cause unexpected behavior. The WAL lock prevents concurrent mddb writers, but cannot prevent external tools from modifying files directly.

### Create

`Create(id, doc)` creates a new document.

1. Stat the canonical file path
2. If file exists: return `ErrExists`
3. Buffer the full document (frontmatter + content) for commit

The `id` is injected into frontmatter automatically; caller-provided frontmatter MUST NOT include `id`.

### Update

`Update(id, patch)` modifies an existing document.

1. Stat the canonical file path
2. If file does not exist: return `ErrNotFound`
3. Read and parse the current document
4. Merge the patch into the document
5. Buffer the merged result for commit

**Patch semantics:**

- Frontmatter: patch keys are merged into existing frontmatter
- Frontmatter: a patch value of `null` removes the key
- Frontmatter: the reserved key `id` MUST NOT be patched (return error if attempted)
- Content: if patch includes content, it replaces existing content; if omitted, content is unchanged

### Delete

`Delete(id)` removes a document.

1. Stat the canonical file path
2. If file does not exist: return `ErrNotFound`
3. Buffer the deletion for commit

### Chained operations

Multiple operations on the same `id` within a transaction are allowed. The **last operation wins**:

| First op | Second op | Net effect |
|----------|-----------|------------|
| Create | Update | Create (with patch applied) |
| Create | Delete | No-op |
| Update | Update | Update (patches merged) |
| Update | Delete | Delete |
| Delete | Create | Create (recreate) |

Implementations MUST track the buffered state per `id` so that:

- `Update` after `Create` merges into the buffered create
- `Delete` after `Create` removes the buffered create (net no-op)
- etc.

### Optional: revision-based conflict detection

Implementations MAY offer revision-checking variants:

- `UpdateIfRevision(id, expectedRevision, patch)`
- `DeleteIfRevision(id, expectedRevision)`

These check that the file's current `mtime_ns` matches `expectedRevision` at call time. If not, return `ErrConflict`.

This enables optimistic concurrency: read a document, note its revision, later update only if unchanged.

## Commit

`Commit()` publishes all buffered operations atomically with respect to **mddb reads**.

### Commit sequence

1. Produce the "net ops" set (one final operation per `id`, after coalescing)
2. If ordered-keys mode is enabled, `Commit()` MUST ensure that any **new IDs** that would require allocating new cache slots can be inserted without violating the ordering constraint.
   - New encoded keys MUST be >= the current tail key (the key bytes of the last allocated slot in the cache, including tombstones) in lexicographic byte order.
   - If multiple new IDs are inserted in a single commit, they MUST be applied to the cache in non-decreasing lexicographic order (or the cache writer must guarantee that behavior).
   - If the ordering constraint would be violated, `Commit()` MUST return an out-of-order insert error (e.g. `ErrOutOfOrderInsert`) **before** modifying the WAL, documents, or cache.
3. `ftruncate(<data-dir>/.mddb/wal, 0)`
4. Write the WAL **body** (JSON Lines) to `.mddb/wal` **without** a footer
5. Mark documents in cache: set `in_wal = 1` for each affected `id` (including creates), then commit the cache
   - If this cache update fails (`ErrFull`, corruption, incompatibility), implementations MUST rebuild/resize the cache (still under the exclusive WAL lock) and retry this step
6. Append the WAL **footer** (commit marker) and (SyncData/SyncAll) `fsync(wal)`
   - **This is the WAL commit point.**
7. Apply net ops to document files:
   - Creates/Updates: write via temp-file + `rename` (atomic)
   - Deletes: remove the file
8. Update the slotcache index to the committed state:
   - Set `revision` to document mtime **after** the atomic rename
   - Update index bytes as needed
   - Clear `in_wal = 0` for each affected `id`
   - If cache update fails (`ErrFull`, corruption, incompatibility), rebuild the cache from documents (still under exclusive WAL lock) and continue
9. `ftruncate(wal, 0)` (cleanup)
10. Release the exclusive WAL lock

**Lock release:** `Commit()` MUST release the exclusive WAL lock before returning, even on error.

### Commit guarantees

If `Commit()` returns success:

- All document files reflect the committed state
- The cache reflects the committed state
- The WAL is empty

If `Commit()` fails at any point:

- No rollback is attempted
- Partial state (WAL bytes, `in_wal` flags, partially-written documents, partially-updated cache) may exist
- Recovery happens on next `Open()` or when a reader triggers recovery

This crash-first design simplifies error handling: the recovery path handles all failure modes.

## Abort

`Abort()` discards all buffered operations and releases the exclusive WAL lock.

After `Abort()`, the transaction handle MUST NOT be used.

## Durability (SyncMode)

mddb distinguishes two crash scenarios:

- **Process crash** (panic, SIGKILL): OS continues, filesystem buffers intact
- **Power loss**: dirty filesystem buffers may be lost

The WAL guarantees **roll-forward correctness** in all modes. **Durability** depends on `fsync`:

| SyncMode | Behavior | Survives process crash | Survives power loss |
|----------|----------|------------------------|---------------------|
| `SyncNone` | No fsync | Yes | No guarantee |
| `SyncData` | fsync WAL and documents | Yes | Yes (files) |
| `SyncAll` | SyncData + fsync directories | Yes | Yes (including renames/unlinks) |

If a required `fsync` fails, `Commit()` MUST return `ErrDurability` even if the logical write succeeded.

**Note:** Because `.mddb/wal` is persistent and not renamed/unlinked on each commit, `SyncAll` does not require a directory fsync for `.mddb/` on every commit. Implementations SHOULD fsync `<data-dir>/.mddb/` when `.mddb/wal` is first created.

## Failure analysis

mddb uses a **crash-first** design: when `Commit()` fails or the process crashes, recovery is responsible for cleanup.

This section analyzes what happens at each failure point and why recovery is always possible.

### Failure during WAL body write

**Point:** Before `in_wal` is published (during WAL body write, before footer)

**State:** `.mddb/wal` may be non-empty but **uncommitted** (no valid footer)

**Recovery:** Truncate WAL to 0 and rebuild cache.

**Why safe:** Documents have not been modified yet (documents are only written after the WAL commit point).

### Failure after setting `in_wal`, before WAL commit marker

**Point:** `in_wal = 1` has been published to the cache, but the WAL has no valid footer.

**State:** Documents are unchanged. Cache entries may be marked `in_wal = 1`.

**Recovery:** Treat WAL as aborted pre-commit: truncate WAL to 0 and rebuild cache from documents.

### Failure after WAL commit marker, during document writes

**Point:** WAL is committed (valid footer + CRC), `in_wal = 1` is published, some documents written, some not

**State:** WAL contains the complete transaction intent.

**Recovery:** Replay WAL to documents (idempotent), finalize cache, truncate WAL.

### Failure during cache update

**Point:** Documents may be fully written, cache partially updated or left in odd generation.

**Recovery:** Replay WAL (idempotent). Then rebuild cache from documents if needed. Truncate WAL.

### Failure after cache finalize, before WAL truncate

**Point:** Everything committed, WAL still committed (non-empty)

**Recovery:** On next `Open()`, replay is a no-op (idempotent) and WAL is truncated.

### Summary

| Failure point | Documents | Cache (`in_wal`) | WAL | Recovery action |
|---|---|---|---|---|
| Before `in_wal` publish (body write, no footer) | unchanged | 0 | uncommitted | truncate WAL, rebuild cache |
| After `in_wal` publish, before footer | unchanged | 1 | uncommitted | truncate WAL, rebuild cache |
| After footer, during doc writes | partial | 1 | committed | replay WAL, finalize cache |
| During cache finalize | committed | 1 → 0 (partial/odd) | committed | replay WAL, rebuild/patch cache |
| After cache finalize, before WAL truncate | committed | 0 | committed | replay no-op, truncate WAL |

The key insight: **the WAL footer is the commit point**. Missing/invalid footer means "treat as aborted".

## Reads during transactions

Transactions are **write-only**. There is no `Tx.Get` or `Tx.Query`.

To read documents, use `Get(id)` or `Query(...)`. These always return **committed state** — they do not see buffered operations.

## Get and Query behavior

`Get(id)` and `Query(...)` MUST treat the reserved `in_wal` flag as a hard barrier. `in_wal = 1` indicates that a cache entry may be in a transitional state (commit/recovery in progress or interrupted) and MUST NOT be used to answer reads without first coordinating recovery.

Because the cache file can be replaced by another process, reads MUST also handle `ErrInvalidated` by remapping the cache and retrying (bounded). See [slotcache integration](010-cache.md#cache-invalidation-and-replacement).

If the cache is missing, corrupt, or incompatible, implementations SHOULD attempt to acquire the exclusive WAL lock, rebuild/refresh the cache from documents, and retry the read. Implementations MAY instead return a classified cache error (for example `ErrNeedsRebuild`, `ErrCacheCorrupt`, `ErrCacheIncompatible`).

### Normal operation (`in_wal = 0`)

- `Get(id)`: reads document file directly
- `Query(...)`: scans the cache and returns matching entries

### Recovery required (`in_wal = 1`)

For `Get(id)`, "encountering `in_wal = 1`" means the cache entry for that `id` has `in_wal = 1`.

For `Query(...)`, "encountering `in_wal = 1`" means `Query` observes `in_wal = 1` for any live entry it **visits** while computing the result set — including entries that do not match the predicate and entries skipped due to `Offset`/`Limit`.

If a read encounters `in_wal = 1`:

1. The read operation MUST attempt recovery by acquiring the **exclusive** WAL lock
2. Under the lock, recovery runs (see [WAL replay protocol](#wal-replay-protocol))
3. The read operation retries

Reads MAY block waiting for the WAL lock (implementation-defined). If the lock cannot be acquired, the read returns a classified lock error (e.g., `ErrBusy`/`ErrLockTimeout`).

### Avoiding `Get()` TOCTOU

To avoid returning a document while its `id` is mid-commit, `Get(id)` MUST treat `in_wal` as a barrier **both before and after** reading the file:

1. Check cache entry `in_wal`
2. Read the document file
3. Re-check cache entry `in_wal`
4. If either check observes `in_wal = 1`, trigger recovery and retry

### Cache `ErrBusy` and crashed writers

If cache reads repeatedly return `ErrBusy` (unable to acquire a stable snapshot), the implementation SHOULD attempt to acquire the exclusive WAL lock.

- If the exclusive lock cannot be acquired, a writer is active; return a transient lock error (or surface `ErrBusy`).
- If the exclusive lock is acquired and cache reads still cannot succeed, treat the cache as crashed/corrupt and rebuild it from documents.

### Why this is safe

The `in_wal = 1` state may be observed briefly during a live commit or recovery, and may persist after a crash until recovery completes. Treating it as a barrier ensures reads never serve a snapshot that is inconsistent with a committed WAL.

Key properties:

- mddb publishes `in_wal = 1` before writing the WAL commit marker, so the crash window where a committed WAL exists but `in_wal` flags are missing does not occur.
- WAL replay is idempotent (net ops with full document content), so replaying an already-applied WAL is a no-op.
- Queries check `in_wal` before predicate evaluation on every visited entry, so they cannot miss WAL-affected entries due to stale index bytes.

## WAL

The WAL (write-ahead log) provides crash-safe roll-forward recovery. It is an internal file at `<data-dir>/.mddb/wal`.

The WAL file MAY be created lazily. Implementations MUST treat a missing WAL file as an empty WAL. The WAL file is also used as the lock target.

The WAL is usually empty. It becomes non-empty during an in-progress commit or after a crash, until recovery completes.

### WAL contents

The WAL contains the **net ops** for the transaction — one final operation per `id` after coalescing.

Each record is either:

- `put`: full document state (frontmatter + content)
- `delete`: document removal

The "net ops" rule makes replay idempotent: replaying the same WAL multiple times yields the same end state.

### WAL record paths

Each WAL record includes the document's **relative path** as computed at commit time.

During replay, the recorded `path` MUST be validated:

- MUST be a relative path
- MUST NOT contain `..`
- MUST end with `.mddb.md`

Additionally, for correctness under layout changes, replay MUST verify that the recorded `path` still matches the current canonical path:

- `path == PathOf(id) + ".mddb.md"`

If this check fails, replay MUST fail with `ErrWALReplay`.

### Format (v1)

The WAL file contains:

- A UTF-8 JSON Lines body (one JSON object per line)
- A fixed-size 32-byte footer (the commit marker)

**Record shapes:**

`put`:
```json
{"op":"put","id":"<id>","path":"<relpath>","frontmatter":{...},"content":"..."}
```

`delete`:
```json
{"op":"delete","id":"<id>","path":"<relpath>"}
```

Implementations MUST ignore unknown fields during replay.

### Footer (commit marker) v1

The footer is fixed-size 32 bytes, little-endian:

```
struct WalFooterV1 {
  u8  magic[8];        // "MDDBWAL1"
  u64 body_len;        // number of bytes in the JSONL body
  u64 body_len_inv;    // bitwise NOT of body_len
  u32 crc32c;          // CRC32-C (Castagnoli) of JSONL body bytes
  u32 crc32c_inv;      // bitwise NOT of crc32c
}
```

Notes:

- The `*_inv` fields provide cheap detection of torn/partial footer writes.
- A WAL is **committed** if the footer is present, self-consistent, and the CRC matches the body.

### Commit-marker presence check

Given WAL size `S`:

- If `S == 0` → WAL empty
- If `S < 32` → WAL uncommitted
- Else read last 32 bytes and validate:
  - `magic == "MDDBWAL1"`
  - `body_len_inv == ^body_len`
  - `crc32c_inv == ^crc32c`
  - `body_len == S - 32`

If these pass, the WAL *claims* to be committed; full CRC verification then requires reading the body and computing CRC32-C.

If footer validation fails, the WAL MUST be treated as **uncommitted**.

### WAL write protocol

WAL writes MUST be atomic with respect to crashes and MUST NOT reach the WAL commit point until `in_wal` markers have been published.

1. `ftruncate(wal, 0)`
2. Write WAL body (JSONL) without footer
3. Publish `in_wal = 1` for all affected IDs in the cache and commit the cache
4. Append WAL footer (commit marker)
5. (SyncData/SyncAll) `fsync(wal)`

### WAL replay protocol

Recovery MUST run only while holding the exclusive lock on `.mddb/wal`.

1. `stat(wal)`
2. If `wal.size == 0`: WAL empty → nothing to replay
3. If `wal.size > 0`:
   - Validate footer and CRC
   - If footer missing/invalid → treat as **uncommitted**:
     - `ftruncate(wal, 0)`
     - rebuild cache from documents
   - If footer valid but CRC mismatch → return `ErrWALCorrupt` (see [Corrupt WAL force recovery](#corrupt-wal-force-recovery))
   - If footer valid and CRC matches → WAL is **committed**:
     1. Read and parse WAL body, collecting affected IDs
     2. Ensure cache is usable (rebuild/replace if missing/corrupt/incompatible/invalidated)
     3. Publish barrier: set `in_wal = 1` for affected IDs, commit cache
     4. Apply WAL records to document files (idempotent)
     5. Finalize cache: set revision/index, clear `in_wal`, commit cache (or rebuild)
     6. `ftruncate(wal, 0)`

If replay fails (corrupt WAL, I/O error, layout mismatch), recovery MUST return an error.

#### Corrupt WAL force recovery

If recovery returns `ErrWALCorrupt`, the WAL footer was present and self-consistent but the CRC32-C did not match the WAL body. In this state, the transaction intent cannot be trusted and roll-forward replay is not possible.

By default, implementations MUST return `ErrWALCorrupt` and MUST NOT modify documents.

Operators MAY choose to restore availability by discarding the WAL **after inspection**, at the cost of potentially losing the last transaction (and potentially leaving the database in a partially-applied state if some document files were already updated prior to the crash).

Force recovery MUST be performed only while holding the exclusive `.mddb/wal` lock:

1. (Optional) Copy `<data-dir>/.mddb/wal` to a separate path for inspection (for example `<data-dir>/.mddb/wal.corrupt.<timestamp>`). The lock inode MUST remain stable: implementations MUST NOT rename, unlink, or replace `.mddb/wal`.
2. Discard the WAL in place: `ftruncate(wal, 0)`.
3. Rebuild or refresh the cache from documents, ensuring all `in_wal` flags are cleared.
4. Resume normal operation.

### WAL lifetime rule

The WAL MUST NOT be truncated to 0 until after reaching a safe post-commit state, except as part of an explicit operator-initiated force recovery for `ErrWALCorrupt` (see [Corrupt WAL force recovery](#corrupt-wal-force-recovery)):

- Normal commit: cache is updated and `in_wal` has been cleared
- Crash recovery: documents are applied and any published `in_wal` markers have been cleared (by cache update or rebuild)

This ensures a crash during cache update (or during recovery) can still be recovered.
