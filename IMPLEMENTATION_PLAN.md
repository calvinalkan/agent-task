# IMPLEMENTATION_PLAN.md

Last updated: 2026-01-19 (Open create/init lock serialization fixed)

This plan tracks remaining work to ensure `pkg/slotcache` is fully compliant with the SLC1 spec in `pkg/slotcache/specs/*`.

## P0 — Spec compliance gaps

- (none currently)

## P1 — Spec alignment / robustness

- [ ] **Align bucket sizing logic with the spec/tech decisions**
  - Replace the float-based sizing helper with the integer formula: `bucket_count = nextPow2(slot_capacity * 2)`.
  - Add overflow checks for `slot_capacity * 2`.

- [ ] **Replace “clamping” integer conversions with explicit validation**
  - Today `safeIntToUint32` / `safeUint64ToInt64` clamp. Instead, reject impossible configurations with `ErrInvalidInput` (e.g. key/index sizes that don’t fit in `u32`, computed file layout that doesn’t fit in `int64`/`int`).

## P2 — Optional hardening / performance

- [ ] **Surface additional corruption early on reads (cheap checks)**
  - Example: if a slot `meta` has reserved bits set (`meta &^ 1 != 0`) under a stable even generation, classify as `ErrCorrupt` (or retry if generation changed).

- [ ] **Reduce allocation/overhead in read retry backoff**
  - Replace `time.After()` in `readBackoff()` with `time.Sleep()` to avoid allocating timers on hot read paths.

- [ ] **Optional Open-time bucket sampling** (spec says MAY)
  - Sample-check a small number of bucket entries for obvious out-of-range slot IDs to fail-fast on common corruptions without scanning the full table.

## Completed

- [x] **Serialize cache file creation/0-byte initialization when locking is enabled**
  - Fixed in `pkg/slotcache/slotcache.go` by routing the “missing file create” and “0-byte init” paths through `openCreateOrInitWithWriterLock()` when `DisableLocking == false`.
  - Added regression tests in `pkg/slotcache/seqlock_concurrency_test.go`:
    - `Test_Open_Returns_ErrBusy_When_Creating_New_File_And_WriterLock_Held`
    - `Test_Open_Returns_ErrBusy_When_Initializing_ZeroByte_File_And_WriterLock_Held`
