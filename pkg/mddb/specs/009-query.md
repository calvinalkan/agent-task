# Query API

`Query` retrieves documents from the database using the `slotcache` index. It does **not** read document file content.

Because documents may be edited externally (bypassing mddb), `Query` can return stale results if the cache hasn't been refreshed. See [Cache invalidation](012-invalidation.md).

## Return type

A query result includes:

- `id` (decoded from key bytes)
- Index values (decoded typed values or raw bytes)
- Optionally: `revision` (mtime ns)

`Query` SHOULD NOT return document content; use `Get` for that.

## Ordering

`Query` returns results in **slot order**:

- After rebuild: lexicographic key order (mddb inserts sorted by key)
- After incremental writes: new entries append at the end (order may diverge from lex order)

This makes pagination (`Offset`/`Limit`) stable within a cache snapshot.

### Ordered-keys mode

mddb MAY be opened with ordered-keys mode enabled. When enabled, slot order is **always** lexicographic key order. Out-of-order inserts return `ErrOutOfOrderInsert`.

See [slotcache integration](010-cache.md) for details.

### Reverse

If `Reverse=true`, results are returned in reverse slot order.

## Options

Typical options:

- `Offset`: skip first N matches
- `Limit`: max matches to return (0 = unlimited)
- `Reverse`: reverse slot order
- `VerifyRevisions`: check file mtime before returning each match

## Freshness check (VerifyRevisions)

When enabled, for each match:

1. Stat the canonical file (`PathOf(id)`)
2. Compare `mtime_ns` to cached revision
3. If missing, not a regular file, or mtime differs → return `ErrCacheStale`

This is optional because it adds one `stat()` per returned match.

## Matchers

Querying scans all slots and applies matcher predicates to each slot's index bytes.

### Built-in predicates

Implementations typically provide predicates for common operations:

- Equality: `Status.Eq("open")`
- Comparison: `Priority.Gte(2)`
- Composition: `Status.Eq("open").And(Priority.Gte(2))`

These decode the index bytes and compare as typed values.

### Custom matcher functions

Implementations MAY support custom matcher functions that receive index bytes and return bool.

Since querying is O(n) over slots regardless, custom functions can decode bytes and apply **any** comparison logic — case-insensitive string matching, float ranges, locale-aware collation, etc. The index bytes are available; how you compare them is up to you.

```
# Pseudocode
Query(func(indexBytes) -> bool {
    name = decodeString(indexBytes, offset=0, len=32)
    return lowercase(name).contains("café")
})
```

## Recovery handling (in_wal)

The reserved `in_wal` flag (see [Index schema](006-index-schema.md#reserved-in_wal-flag)) is a per-entry barrier that prevents `Query` from serving results from a cache snapshot that might not represent a fully committed state.

During query evaluation, `Query` MUST inspect the `in_wal` flag for every live entry it **visits** while computing the result set. This check MUST occur **before** evaluating the user-supplied matcher predicate, and MUST apply even to:

- entries that do not match the predicate, and
- entries that would be skipped due to `Offset`/`Limit`.

If `in_wal = 1` is encountered for any visited entry, `Query` MUST NOT return results from that snapshot. Instead it MUST:

1. Trigger recovery (acquire the exclusive WAL lock on `.mddb/wal`, replay/truncate the WAL as needed, and rebuild/refresh the cache if needed),
2. If recovery fails, return an error,
3. Retry the query from the beginning.

This rule is required because during a commit or crash recovery the cache may temporarily contain `in_wal = 1` entries whose other index bytes still reflect the pre-transaction state. Checking `in_wal` only on matched/returned results can therefore cause false negatives.

**Predicate visibility:** Implementations MUST NOT expose the reserved `in_wal` byte to caller predicates. Predicates operate only on the user-defined index bytes (the schema-defined portion), excluding the trailing `in_wal` byte.

**Early termination:** `Query` MAY stop scanning once it has found `Offset+Limit` matches (in the chosen scan order). It only needs to enforce the `in_wal` barrier for entries it actually visits.

This is correct because slotcache v1 scans are ordered by **slot ID** and slot IDs are **append-only and stable** across commits: `Put` updates an existing key in place, `Delete` tombstones the existing slot, and new keys allocate new slots at the end. Therefore, unvisited entries are strictly later in scan order and cannot affect the returned window. (See [`slotcache` semantics](../../slotcache/specs/003-semantics.md).)

If a future cache implementation reorders or compacts slots (changing slot positions), `Query` MUST either disable early termination or otherwise ensure it cannot miss `in_wal = 1` entries that could affect the returned results.

If the underlying cache file is being replaced by another process, `slotcache` may return `ErrInvalidated`. In that case, `Query` MUST close/unmap and re-open the cache and retry (bounded).

```
Query(opts, predicate):
  for retries := 0; retries < MaxRetries; retries++ {
    sawInWal := false

    results, err := cache.Scan(opts, func(indexBytesWithInWal []byte) bool {
      inWal := indexBytesWithInWal[len(indexBytesWithInWal)-1]
      if inWal == 1 {
        sawInWal = true
        return false
      }

      userIndex := indexBytesWithInWal[:len(indexBytesWithInWal)-1]
      return predicate(userIndex)
    })

    if err != nil {
      if err == ErrInvalidated {
        reopenCache()
        continue // retry (bounded)
      }
      return nil, err
    }

    if sawInWal {
      err := triggerRecovery() // acquires exclusive `.mddb/wal` lock; may block; returns lock error on contention
      if err != nil {
        return nil, err
      }
      continue // retry after recovery
    }

    return results, nil
  }

  return nil, ErrBusy
```

The `in_wal = 1` state may be observed briefly during a live commit or recovery, and may persist after a crash until recovery completes. In normal operation, this check adds negligible overhead (one byte comparison per visited entry).

See [Transactions](008-transactions.md#get-and-query-behavior) for the full protocol.

## Isolation and contention

`Query` returns a consistent snapshot, but may fail under write contention.

Internally, slotcache retries if the cache generation changes during the scan. After a bounded number of retries, slotcache returns `ErrBusy`.

Implementations MAY surface this as `ErrBusy`, or MAY treat it as a signal to coordinate using the exclusive WAL lock (wait for any writer/recovery, then retry). See [Transactions](008-transactions.md#get-and-query-behavior).

For long-running queries or multiple queries that must see consistent state, use a read transaction:

```
rtx := db.BeginReadTx()
defer rtx.Close()

results1 := rtx.Query(predicate1)  // never returns ErrBusy
results2 := rtx.Query(predicate2)  // sees same snapshot as results1
```

See [Isolation and snapshots](007-lifecycle.md#isolation-and-snapshots) for details.

## Complexity

mddb's query complexity depends on which slotcache APIs are used:

| slotcache API | Complexity | Notes |
|---------------|------------|-------|
| `Scan` | O(N) | Sequential scan over all slots |
| `ScanRange` (ordered mode) | O(log N + R) | Binary search to start, scan R entries in range |
| `ScanPrefix` | O(N) | Full scan with prefix filter |
| `Get` | O(1) | Hash table lookup |

**N** = total live entries, **R** = entries in the key range.

`ScanRange` also supports a filter function, applied after the binary search narrows to the range. So a range query with an index filter is O(log N + R), where the filter runs on R candidates.

mddb chooses the appropriate slotcache API based on the query:
- Key range constraints → `ScanRange` (if ordered mode enabled)
- Index-only filters → `Scan` with filter function
- Key range + index filter → `ScanRange` with filter function
