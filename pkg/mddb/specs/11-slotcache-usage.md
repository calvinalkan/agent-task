# slotcache integration

This document defines how mddb uses **slotcache v1 (SLC1)** to implement `<data-dir>/.cache`.

For the underlying cache file format and concurrency rules, see [slotcache v1](../../slotcache/specs/slotcachev1.md).

## slotcache options

mddb configures slotcache roughly as:

- `Path`: `<data-dir>/.cache`
- `KeySize`: 64
- `IndexSize`: derived from schema (see [schema](schema.md))
- `UserVersion`: schema hash (64-bit)

Capacity strategy is an mddb concern (see below).

## Key encoding

mddb stores keys as fixed-size bytes:

- encode the key as UTF-8 bytes
- reject if byte length > 64
- store bytes into a 64-byte buffer
- NUL-pad the remainder

This matches the mddb key rules in [document format](document-format.md).

## Revision

slotcache stores an opaque `int64 revision` per slot.

mddb SHOULD store a revision that is useful for staleness detection, typically:

- the document file `mtime` as unix nanoseconds (`time.Unix(0, stat.Mtim.Nsec)`)

This can be used by higher layers to cheaply verify that a cached entry still corresponds to a particular file state.

## Index bytes

Index bytes are exactly `IndexSize` bytes, produced by encoding the schema fields in order.

They are stored verbatim in slotcache slot records.

## Cache build and update strategy

There are two reasonable strategies:

### Strategy A: incremental slotcache updates

- On `Commit()`, update only the affected keys using slotcache writer session:
  - `Put(key, revision, indexBytes)` for creates/updates
  - `Delete(key)` for deletes
- Advantage: fast commits when few docs change.
- Tradeoff: `slot_highwater` grows as new keys are created; deletes create tombstones.

### Strategy B: rebuild cache on every commit

- On `Commit()`, rebuild the cache from scratch (scan all docs).
- Advantage: compact cache, no tombstones, can write slots in key order.
- Tradeoff: commit cost O(total docs).

This spec does not force one strategy yet; see [open questions](open-questions.md).

## Capacity strategy

slotcache v1 requires a fixed `SlotCapacity` at creation.

mddb SHOULD choose capacity during rebuild as:

- `SlotCapacity = ceil(doc_count * growth_factor)` (e.g., 1.25x)

If incremental updates are used, mddb SHOULD rebuild with a larger capacity when:

- `ErrFull` occurs, or
- `slot_highwater` approaches `slot_capacity`, or
- excessive tombstones accumulate (implementation-defined)

Because the cache is derived, rebuilding is always safe.

## Locking integration

mddb already enforces a single writer via its own lock.

Therefore, mddb MAY open slotcache with `LockNone` and rely on the mddb writer lock.

If slotcache locking is enabled, mddb MUST ensure lock ordering is consistent to avoid deadlocks.
