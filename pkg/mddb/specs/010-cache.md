# slotcache integration

mddb uses `slotcache` as a throwaway binary index over the markdown documents.

For cache lifecycle (open, rebuild, WAL interaction), see [Lifecycle](007-lifecycle.md).

## Core invariants

When the cache is valid and reflects a committed state (i.e., `in_wal = 0` for all entries):

- Each cached entry corresponds to exactly one canonical document file on disk.
- The cache entry key bytes correspond to the document `id` (as defined in [IDs and key encoding](004-id.md)).
- The cache entry `revision` is the document file's mtime in nanoseconds (as observed by mddb at indexing time).
- The cache entry `index` bytes are the schema-encoded representation of indexed fields.

While a commit or recovery is in progress, the cache MAY temporarily contain entries with `in_wal = 1`.
In that transitional state, the per-entry invariants above may not hold for affected IDs (for example, creates may be present in the cache before the document file is written).
Readers MUST treat `in_wal = 1` as a hard barrier and trigger recovery before returning data.
When the cache is invalid or missing:

- Reads that require the cache (e.g., `Query`) SHOULD attempt to rebuild/refresh the cache (typically by acquiring the exclusive WAL lock) and retry. Implementations MAY instead return a classified error (e.g., `ErrNeedsRebuild`, `ErrCacheCorrupt`, `ErrCacheIncompatible`).
- Reads that access the source of truth directly (e.g., `Get`) MUST remain correct.

## Locking

mddb opens `slotcache` with internal locking disabled. All cache writes (commit, recovery, rebuild, cache swap) are serialized while holding an **exclusive** lock on `<data-dir>/.mddb/wal`. See [Lifecycle](007-lifecycle.md#concurrency-model).

## Cache invalidation and replacement

Because `slotcache` is memory-mapped, replacing the cache file via `rename()` can leave other processes reading from the old inode indefinitely.

To support safe replacement, `slotcache` provides a first-party invalidation mechanism:

- An invalidated cache causes `slotcache` operations (including `Open`) to return `ErrInvalidated`.

mddb uses this to implement safe cache swaps.

### Reader behavior (`ErrInvalidated`)

If any cache operation used by mddb (e.g., `Get`, `Scan`, `ScanRange`) returns `ErrInvalidated`, the mddb implementation MUST:

1. Close/unmap its current cache handle
2. Re-open `<data-dir>/.mddb/cache.slc`
3. Retry the higher-level operation from the beginning

Retries MUST be bounded; if the cache remains invalidated, mddb SHOULD return `ErrBusy` (another process is replacing the cache).

### Writer behavior (safe cache swap)

Whenever mddb is about to replace `<data-dir>/.mddb/cache.slc` (rebuild, resize, compaction), and the current process has an open cache handle to the existing file, it MUST:

1. Hold the mddb WAL lock (exclusive `flock` on `.mddb/wal`)
2. Call `cache.Invalidate()` on the existing cache
3. Publish the new cache file via atomic rename (`cache.slc.tmp → cache.slc`)

This ensures long-lived readers that still have the old cache mmapped will see `ErrInvalidated` and remap to the new cache.

If the existing cache cannot be opened (missing/corrupt/incompatible), invalidation cannot be applied. In that case, mddb MAY proceed with rebuild and publish the new cache; callers with long-lived handles SHOULD reopen the database to pick up the new cache.

## Cache file

The cache file is stored at:

- `<data-dir>/.mddb/cache.slc`

This is a `slotcache` v1 (SLC1) file.

## What is stored

For each canonical document:

- `slotcache.key` = the fixed-size encoded form of `id` (see [IDs and key encoding](004-id.md))
- `slotcache.revision` = document file mtime in nanoseconds
- `slotcache.index` = schema-encoded index bytes + `in_wal` flag (see [Index schema](006-index-schema.md))

The `in_wal` flag is a reserved byte at the end of the index, used for crash recovery. See [Transactions](008-transactions.md#get-and-query-behavior).

mddb MUST NOT store document content in the cache.

## Cache options

When opening the cache, mddb MUST configure `slotcache` with:

- `KeySize == IDSpec.KeySize`
- `IndexSize == schema.IndexSize + 1` (the extra byte is the reserved `in_wal` flag)

### Capacity

`slotcache` files are fixed-capacity. Slot IDs are append-only and tombstones are not reused.

Therefore, mddb MUST have a strategy for:

- choosing an initial `slot_capacity`,
- rebuilding to compact tombstones,
- rebuilding/resizing when the cache is full.

A recommended default is:

- `slot_capacity = nextPow2(max(1024, ceil(doc_count * 1.25)))`

where `doc_count` is the number of canonical documents discovered during rebuild.

mddb SHOULD NOT pin `slot_capacity` during normal opens (i.e., it should allow opening caches with different capacities).

### Compatibility (UserVersion)

mddb MUST ensure that a cache built with one schema/ID/layout configuration is not accidentally reused under a different one.

Implementations SHOULD set `slotcache.UserVersion` to a stable hash over:

- the index schema definition (names, types, sizes, defaults, order)
- the IDSpec definition (KeySize + validation mode)
- a caller-supplied **LayoutID** (a stable identifier for `PathOf` / `IdFromPath` behavior)
- any cache-ordering mode bits (e.g., ordered-keys mode enabled)

If `slotcache` returns an incompatibility error at open, mddb MUST rebuild the cache.

## Cache ordering

mddb uses the cache scan order as the ordering for `Query()`.

### Rebuild insertion order

During a full rebuild, mddb MUST insert documents into a new cache in **lexicographic order of their encoded keys**.

This makes rebuilds deterministic and gives stable initial pagination.

### Incremental insertion order

During a normal transaction commit, mddb applies net ops to the cache.

- Updating an existing `id` updates the existing slot in place.
- Inserting a new `id` appends a new live slot.
- Deleting an `id` marks its slot as a tombstone.

Therefore, incremental writes can make scan order differ from lexicographic key order.

### Optional: ordered-keys mode

mddb MAY support an optional “ordered-keys” mode by creating the `slotcache` file with `FLAG_ORDERED_KEYS` enabled.

When enabled:

- the cache scan order is guaranteed to be lexicographic key order,
- `slotcache` rejects commits that would append out-of-order keys.

Because this condition is often resolvable by choosing a different (larger) `id` (for example, generating a new UUIDv7), implementations SHOULD surface an out-of-order insert failure (`ErrOutOfOrderInsert` or a classified equivalent) to the caller so they can retry.

Implementations MAY rebuild the cache as a remediation, but rebuild is not required for correctness in ordered-keys mode.

This mode is intended to preserve the invariant:

- cache scan order == lexicographic key order

## Query behavior by mode

In ordered-keys mode, slotcache can use **binary search** for many key-based operations instead of O(N) scans:

| Query type | Non-ordered | Ordered-keys |
|------------|-------------|--------------|
| Filter on index fields | O(N) | O(N) |
| Key prefix | O(N) | O(log N + R) |
| Key range | O(N) | O(log N + R) |
| Key range + Reverse + Limit | O(N) | O(log N + L) |
| Single key lookup | O(1) | O(1) |

**N** = total live entries, **R** = entries in range/prefix, **L** = limit.

In ordered-keys mode:
- Key range and prefix queries binary search to the start position, scan only matching entries
- `Reverse` can walk backwards from the end bound
- `Limit` enables early termination after L results

In non-ordered mode, all scans are O(N) because slot order doesn't match key order.

**Note:** `Offset` cannot use binary search in either mode because tombstones mean slot positions don't correspond to result positions. Offset is always applied after collecting matching entries.

### When to use ordered-keys mode

Ordered-keys mode is **preferred whenever possible** due to the performance benefits.

To use ordered mode, IDs must be inserted in lexicographic order. Strategies include:

- **UUIDv7** - time-ordered UUIDs that are both unique and naturally ordered
- **Auto-increment integers** - fetch the current highwater within a transaction, assign next ID
- **Timestamp-prefixed IDs** - e.g., `2024-01-15/abc123`

The only reason to use non-ordered mode is when IDs are inherently unordered (e.g., random UUIDv4) and cannot be changed.

## Maintenance and auto-rebuild

Because slot IDs are not reused, a long-lived cache can become inefficient or full.

Implementations SHOULD rebuild/resize the cache when any of the following occur:

- `slotcache` returns `ErrFull` during commit
- tombstone density grows high (implementation-defined threshold)

In ordered-keys mode, an out-of-order insert indicates an ID-ordering issue. Implementations SHOULD surface this condition to the caller so they can retry with a different (larger) id. Implementations MAY rebuild the cache as a remediation, but rebuild is not required for correctness.

Rebuild is always safe because the source-of-truth is the documents.

## Prefix/range primitives

`slotcache` defines a prefix-match scan and ordered range iteration (when ordered-keys mode is enabled). mddb MAY use these primitives to implement fast ID-prefix resolution or bounded scans.

This is optional and does not change the externally visible semantics of mddb.

## Cache rebuild responsibility

`slotcache` cannot rebuild itself because it does not know how to enumerate and parse documents.

mddb MUST be responsible for rebuilding the cache from the source-of-truth files.
