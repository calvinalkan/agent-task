# IMPLEMENTATION_PLAN.md

Last updated: 2026-01-19 (Bucket sizing aligned with spec)

This plan tracks remaining work to ensure `pkg/slotcache` is fully compliant with the SLC1 spec in `pkg/slotcache/specs/*`.

## P0 — Spec compliance gaps

- (none currently)

## P1 — Spec alignment / robustness

- (none currently)

## P2 — Optional hardening / performance

- [ ] **Surface additional corruption early on reads (cheap checks)**
  - Example: if a slot `meta` has reserved bits set (`meta &^ 1 != 0`) under a stable even generation, classify as `ErrCorrupt` (or retry if generation changed).

- [ ] **Reduce allocation/overhead in read retry backoff**
  - Replace `time.After()` in `readBackoff()` with `time.Sleep()` to avoid allocating timers on hot read paths.

- [ ] **Optional Open-time bucket sampling** (spec says MAY)
  - Sample-check a small number of bucket entries for obvious out-of-range slot IDs to fail-fast on common corruptions without scanning the full table.

## Completed

- [x] **Replace "clamping" integer conversions with explicit validation**
  - Replaced `safeIntToUint32` / `safeUint64ToInt64` / `safeUint64ToInt` clamping functions with checked versions (`intToUint32Checked`, `uint64ToInt64Checked`, `uint64ToIntChecked`) that return `(value, ok)`.
  - Added upfront validation in `Open()` for KeySize and IndexSize to fit in uint32.
  - Added `validateFileLayoutFitsInt64()` to reject configurations where computed file size would overflow int64 (required for mmap/ftruncate).
  - Added validation in `validateAndOpenExisting()` for computed file size overflow.
  - Added tests: `Test_Open_Returns_ErrInvalidInput_When_KeySize_Exceeds_Uint32Max`, `Test_Open_Returns_ErrInvalidInput_When_IndexSize_Exceeds_Uint32Max`, `Test_Open_Returns_ErrInvalidInput_When_FileLayout_Exceeds_Int64Max`.

- [x] **Align bucket sizing logic with the spec/tech decisions**
  - Replaced the float-based `computeBucketCount(slotCapacity, loadFactor)` with the integer formula: `bucket_count = nextPow2(slot_capacity * 2)`.
  - Added overflow checks for `slot_capacity * 2` in `Open()` — rejects capacities > maxUint64/2 with `ErrInvalidInput`.
  - Removed the unused `defaultLoadFactor` constant.
  - Updated tests in `format_test.go` and `open_validation_test.go`.

- [x] **Serialize cache file creation/0-byte initialization when locking is enabled**
  - Fixed in `pkg/slotcache/slotcache.go` by routing the "missing file create" and "0-byte init" paths through `openCreateOrInitWithWriterLock()` when `DisableLocking == false`.
  - Added regression tests in `pkg/slotcache/seqlock_concurrency_test.go`:
    - `Test_Open_Returns_ErrBusy_When_Creating_New_File_And_WriterLock_Held`
    - `Test_Open_Returns_ErrBusy_When_Initializing_ZeroByte_File_And_WriterLock_Held`
