---
schema_version: 1
id: d5s4fn0
status: open
blocked-by: []
created: 2026-01-22T15:52:52Z
type: feature
priority: 2
---
# Slotcache scan strategy and buffer reuse improvements

## Description
- Reverse scans in ordered-keys mode currently walk the mmap backwards (doCollectReverse/doCollectRangeReverse) for early termination.
- Backwards access can defeat OS readahead on cold caches; for predicates/selective filters it often scans most of the range anyway.
- Scan APIs must return owned bytes (snapshot semantics), so per-entry allocations/copies are a hotspot.
- We want a design that keeps a single scan API surface but allows buffer reuse and better scan strategy control.

## Design
- Replace `ScanOptions.Reverse bool` with `ScanOrder` enum: `ScanAsc` / `ScanDesc`.
  - Asc/Desc defines output order (slot order; key order for ordered-keys).
- Add optional `ScanStrategy` enum: `ScanAuto` (default), `ScanForward`, `ScanBackward`.
  - `ScanBackward` only meaningful for `ScanDesc`.
  - `ScanAuto` uses heuristics (see below) but callers can override.
- Introduce a reusable scan buffer via a `Scanner` helper (avoid Scan/ScanInto duplication):
  - `Cache.Scanner()` returns a `Scanner` with internal `ScanBuffer` (arena + scratch).
  - Scanner is not goroutine-safe; one per goroutine.
  - Results are valid until the next scan on that same scanner; callers that need persistence must copy.
- ScanBuffer internals (reusable scratch):
  - `ScanBuffer` holds `entries []Entry`, `keyArena`, `indexArena`, and optional `slotRing`.
  - `Reset()` clears entries and resets arenas; does not shrink allocations by default.
  - Returned entries are slices into the arenas; data valid until buffer reuse.
- Arena implementation:
  - Chunked allocator (no append on a single slice) to avoid invalidating existing slices.
  - `alloc(n int)` returns a slice; allocate new chunk when remaining < n.
  - If `Limit>0`, preallocate a single chunk sized for `need := Offset+Limit` entries when feasible.
- Ring buffer for tail selection (forward scan + reverse output):
  - `slotRing` stores `[]uint64` slot IDs (or offsets) for the last `need` matches.
  - `push(id)` appends until full, then overwrites oldest and advances `head`.
  - After the scan, `ordered()` returns IDs in ascending order (oldest→newest).
  - For `Order=Desc`, iterate IDs in reverse to materialize output without an extra reverse.
- Allocation/materialization strategy:
  - Default: copy key/index into arenas as entries are accepted.
  - Ring path (Reverse+Limit): scan forward, track slot IDs only; after scan, materialize retained entries into arenas under stable generation.
- Scan strategy heuristics:
  - For ordered range/prefix (offset 0), compute range size via binary search.
  - `ScanAuto` chooses backward scan when `Order=Desc`, `Limit>0`, `Filter==nil`, and range size is large relative to `Offset+Limit`.
  - Otherwise scan forward sequentially; if `Order=Desc`, reverse results in-memory (ring buffer).
  - For predicate scans with unknown selectivity, prefer forward sequential scan.
- Maintain current snapshot semantics and error behavior; no streaming/borrowed API changes.

## Acceptance Criteria
- `ScanOptions` exposes `Order` and optional `Strategy`; old `Reverse` removed or deprecated with migration.
- `Cache.Scanner()` implemented; buffer reuse works across Scan/ScanRange/ScanMatch without new `ScanInto` variants.
- Scan results remain owned copies and stable after scan completion (until scanner reused).
- Arena allocation removes per-entry allocations in scans.
- ScanBuffer implements chunked arenas + slotRing; ring path only materializes retained entries (no N×copy churn).
- Reverse scans use `ScanAuto` heuristic + ring buffer path for forward sequential scans; tests cover correctness for range/prefix/predicate.
- Docs updated for scan order/strategy and scanner lifetime.
