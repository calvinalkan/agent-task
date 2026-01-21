# Error Handling Hardening Plan

## Context

We want to eliminate silent/sentinel return values in the slotcache core and
always return real errors for invalid inputs or impossible states. This aligns
with the spec's "fail fast on corruption/mismatch" philosophy and prevents
silent corruption when using mmap'd data.

## Goals

- No more "0/false/nil means error" in core helpers.
- All invalid inputs and impossible states return real errors.
- Errors are propagated to public APIs (ErrInvalidInput/ErrCorrupt/ErrWriteback
  as appropriate) instead of being silently ignored.

## Non-goals

- No format/spec changes.
- No behavioral changes outside error handling (e.g., no new features).

---

## Phase 1 — format.go helper signatures

Replace sentinel return patterns with real errors.

### Tasks
- [x] Update conversion helpers to return errors instead of (value, ok):
  - `intToUint32Checked` — now returns `(uint32, error)` wrapping `ErrInvalidInput`
  - `uint64ToInt64Checked` — now returns `(int64, error)` wrapping `ErrInvalidInput`
  - `uint64ToIntChecked` — now returns `(int, error)` wrapping `ErrInvalidInput`
  - All call sites in `slotcache.go` and `writer.go` updated to use `err != nil` checks
- [x] Make `computeSlotSize` return `(uint32, error)` (remove implicit `0` fallback).
  - Removed sentinel wrapper; renamed `computeSlotSizeChecked` to `computeSlotSize`
  - All call sites updated to handle errors
- [x] Make `computeBucketCount` return `(uint64, error)` (reject `slotCapacity == 0`
  and overflow instead of returning `2/0`).
  - Removed sentinel wrapper; renamed `computeBucketCountChecked` to `computeBucketCount`
  - All call sites updated to handle errors
- [x] Update `msyncRange` to return an error for empty/invalid ranges (no nil on
  invalid input).
  - Returns `ErrInvalidInput` for: length <= 0, offset < 0, offset >= len(data)
  - Callers must validate ranges before calling (invalid ranges indicate bugs)

### Files
- `pkg/slotcache/format.go`

---

## Phase 2 — format call-site propagation

Update call sites to handle new error-returning helpers.

### Tasks
- [x] Propagate conversion errors in `Open`, `createNewCache`, and
  `initializeEmptyFile`.
- [x] Update `newHeader` to return `(slc1Header, error)` (or accept validated values)
  and propagate errors from `computeSlotSize/computeBucketCount`.
- [x] Update `encodeSlot` to handle `computeSlotSize` errors (or refactor to accept
  precomputed slot size).
  - Refactored `encodeSlot` to accept precomputed `slotSize` parameter
  - Error handling now happens at cache initialization time, not during slot encoding
- [x] Replace any uses of `computeSlotSize` / `computeBucketCount` that assumed
  sentinel values.
- [x] Update `validateAndOpenExisting`, `validateFileLayoutFitsInt64`,
  `sampleBucketsForCorruption` to handle conversion errors explicitly.

### Files
- `pkg/slotcache/slotcache.go`
- `pkg/slotcache/format.go`

---

## Phase 3 — writer.go error propagation

Eliminate silent failures in commit/writeback paths.

### Tasks
- [x] Propagate conversion errors in Commit when calculating bucket msync ranges.
  - Conversion errors set `msyncFailed = true`, resulting in `ErrWriteback`
- [x] Make dirty-range tracking (`markSlotDirty`, `markBucketDirty`) return errors
  instead of silently skipping when conversions fail.
  - Both functions return `ErrCorrupt` on conversion failure
  - All call sites in Commit propagate these errors
- [x] Surface corruption in `findLiveSlotLocked` when bucket entries point past
  `slot_highwater` (return error instead of skipping).
  - Returns `ErrCorrupt` when slot_id >= highwater
- [x] Decide how these new errors map to `ErrCorrupt` vs `ErrWriteback` and
  propagate consistently.
  - `markSlotDirty`/`markBucketDirty`: `ErrCorrupt` (indicates data structure corruption)
  - `findLiveSlotLocked`: `ErrCorrupt` (indicates bucket/slot inconsistency)
  - Commit msync conversion failures: `ErrWriteback` (durability issue, not corruption)

### Files
- `pkg/slotcache/writer.go`

---

## Phase 4 — tests and expectations

Update tests for new error behavior.

### Tasks
- [x] Update `format_test.go`:
  - [x] `computeSlotSize` / `computeBucketCount` tests to expect errors instead of
    sentinel values.
  - [x] `msyncRange` tests to expect errors on invalid/empty ranges.
- [x] Update any tests that call `newHeader` / `encodeSlot` directly.
- Audit other tests for use of the old helper signatures.

### Files
- `pkg/slotcache/format_test.go`
- Any other tests affected by signature changes

---

## Phase 5 — validation

- `make test`
- `make lint`

---

## Acceptance Criteria

- No helper in `format.go` returns sentinel values for invalid input.
- `msyncRange` never returns nil for invalid ranges.
- Writer commit paths surface conversion/corruption errors.
- All tests updated and passing.
