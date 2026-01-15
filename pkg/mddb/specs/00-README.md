# mddb specifications

This folder is the working spec set for **mddb** (Markdown Database) and its on-disk index cache **slotcache v1**.

- mddb stores the **source of truth** as human-readable `*.mddb.md` files.
- mddb stores a **throwaway binary cache** in `<data-dir>/.cache` to accelerate reads.
- The cache format and concurrency rules are defined by **slotcache v1**.

## Table of contents

- [Overview](overview.md)
- [Design rationale](rationale.md)
- [Public API](api.md)
- [Directory layout](layout.md)
- [Document format](document-format.md)
- [Schema and index encoding](schema.md)
- [Filter API](filter-api.md)
- [Transactions and locking](transactions.md)
- [Write-ahead log](wal.md)
- [Cache management and invalidation](cache.md)
- [slotcache integration](slotcache-usage.md)
- [Error model](errors.md)
- [Watcher](watcher.md)
- [Limitations](limitations.md)
- [Open questions](open-questions.md)

## Dependency specs

- [slotcache v1 (SLC1)](../../slotcache/specs/slotcachev1.md)
