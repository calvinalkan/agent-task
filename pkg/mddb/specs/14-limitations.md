# Limitations and tradeoffs

## Concurrency

- Single writer only (exclusive lock).
- No MVCC/snapshot isolation.
- Readers may observe the pre-commit state while a writer is committing.

## Transactions

- No rollback; crash recovery is roll-forward from WAL.

## Storage layout

- Flat directory only.
- YAML frontmatter only (v1).

## Cache

- The cache is derived and may be rebuilt at any time.
- External edits can make the cache stale until invalidated/rebuilt.
- slotcache v1 uses a fixed slot capacity; very high churn may require rebuild/resize.

## Filesystem assumptions

- Designed for local POSIX-like filesystems with atomic rename.
- Not designed/tested for network/distributed filesystems or cloud-sync folders.
