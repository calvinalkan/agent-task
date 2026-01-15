# Open questions

These are decisions that materially affect the mddb spec and implementation.

## 1) Filter ordering

The rough mddb design doc assumes that `Filter()` results are returned in **lexicographic key order** for stable pagination.

slotcache scanning naturally yields **slot order** (slot id order).

Decision needed:

- Keep key order as the default (requires sorting matches), or
- Switch default ordering to slot order and make key-order sorting opt-in.

## 2) Cache update strategy

Should commits:

- update `.cache` incrementally via slotcache `Put/Delete`, or
- rebuild `.cache` from scratch on every commit?

This impacts performance, ordering, and compaction.

## 3) Data caching

The rough design doc includes optional caching of arbitrary per-doc data (serialized via gob).

slotcache v1 is index-only and stores fixed-size bytes per slot.

Decision needed:

- Drop/defers data caching (v1 index-only), or
- Introduce a separate blob store / second cache file, or
- Require cached data to be fixed-size and stored inside index bytes.

## 4) Locking integration

mddb already has a writer lock.

Decision needed:

- Open slotcache with `LockNone` and rely solely on the mddb lock, or
- Also enable slotcache locking (careful lock ordering).

## 5) External staleness detection

The current approach is an external protocol + optional watcher.

Decision needed:

- Is it acceptable that `Filter()` may return stale results until invalidated, or
- Should mddb add an optional runtime check (e.g., compare cached revision vs file mtime for returned matches)?
