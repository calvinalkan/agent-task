# IMPLEMENTATION_PLAN.md (slotcache)

This document is the master implementation plan for upcoming **slotcache** work in this repo.

It merges:
- **First‑party cache invalidation** (header `state` + `ErrInvalidated` + `Cache.Invalidate()`), and
- **Caller-owned header metadata** (`user_flags` + `user_data`) with dedicated APIs, and
- **Cache.Generation()** read API for cheap change detection, and
- **OrderedKeys scan optimizations**

The goal is to land changes in small, coherent phases that keep tests green.

---

## Decisions locked in (format v1)

### Header layout changes (still 256 bytes)

We are editing the **v1** header layout (no backwards compatibility constraints; not deployed).

Header word at `0x074` is no longer “reserved”; it becomes cache state:

- `0x074: state u32` (slotcache-owned)
  - `STATE_NORMAL = 0`
  - `STATE_INVALIDATED = 1` (terminal)
  - Unknown values MUST be rejected as `ErrIncompatible`.

Caller-owned metadata region (subset of the old reserved bytes):

- `0x078: user_flags u64` (caller-owned, opaque)
- `0x080: user_data[64]byte` (caller-owned, opaque)

Slotcache-owned reserved tail (kept for future v1 extension without bumping version):

- `0x0C0..0x0FF: reserved[64]byte` MUST be 0
  - If any of these bytes are non-zero: `ErrIncompatible`.

CRC rules remain unchanged:
- `header_crc32c` covers the entire 256-byte header (including `state`, `user_flags`, `user_data`, and reserved tail)
- CRC computation still zeroes only `generation` and the CRC field itself.

---

## Phase 1 — Specification updates (format + semantics) ✓

- [x] Update `pkg/slotcache/specs/002-format.md`:
  - [x] Replace `reserved_u32` at `0x074` with `state u32` + document values.
  - [x] Replace “reserved bytes 0x078..0x0FF MUST be 0” with:
    - `user_flags u64` at `0x078`
    - `user_data[64]byte` at `0x080`
    - `reserved[64]byte` at `0x0C0..0x0FF` MUST be 0
  - [x] Clarify that CRC covers user header bytes.

- [x] Update `pkg/slotcache/specs/003-semantics.md`:
  - [x] Add `ErrInvalidated` to the error model + recovery guidance.
  - [x] Specify `Cache.Invalidate()` behavior and publish protocol (odd/even generation + CRC update + optional msync barriers).
  - [x] Specify user header semantics:
    - stable snapshot reads (seqlock correct-or-retry)
    - writes published via Writer commit (field-level last-write-wins)
    - untouched field preservation.
  - [x] Document `Cache.Generation()` semantics (stable even snapshot, ErrBusy on contention, ErrInvalidated/ErrClosed) and add it to the required operations list.

(Keep `pkg/slotcache/specs/README.md` as optional follow-up: brief mention in quick reference.)

---

## Phase 2 — Public API surface ✓

File: `pkg/slotcache/api.go`

- [x] Add new sentinel error:
  - [x] `ErrInvalidated`

- [x] Extend `Cache` interface:
  - [x] `Invalidate() error`
  - [x] `UserHeader() (UserHeader, error)`
  - [x] `Generation() (uint64, error)`

- [x] Extend `Writer` interface:
  - [x] `SetUserHeaderFlags(flags uint64) error`
  - [x] `SetUserHeaderData(data [UserDataSize]byte) error` (where `UserDataSize = 64`)

- [x] Add exported types/constants:
  - [x] `const UserDataSize = 64`
  - [x] `type UserHeader struct { Flags uint64; Data [UserDataSize]byte }`

Notes:
- We intentionally do **not** add invalidation ops to the model-vs-real behavior harness (see Phase 6/7).
- Stub implementations added in `slotcache.go` and `writer.go` (panic until Phase 4-6).

---

## Phase 3 — Format plumbing + Open/validation updates ✓

Files: `pkg/slotcache/format.go`, `pkg/slotcache/slotcache.go`

- [x] Update header offsets/constants:
  - [x] `offReservedU32` → `offState`
  - [x] Add `offUserFlags`, `offUserData`, and `offReservedTailStart` constants.

- [x] Header encoding/creation:
  - [x] `newHeader(...)` sets `state=STATE_NORMAL` and zeroes user header + reserved tail.
  - [x] `encodeHeader()` writes state + user fields.

- [x] Reserved-byte helpers/tests:
  - [x] Update `hasReservedBytesSet` to only check `0x0C0..0x0FF`.
  - [x] Adjust `pkg/slotcache/format_test.go` to flip a tail byte instead of user header bytes.

- [x] Open/validation (`validateAndOpenExisting`):
  - [x] Preserve “odd generation” handling ordering (must not misclassify in-progress commits).
  - [x] Validate `state` is in {0,1}; reject unknown as `ErrIncompatible`.
  - [x] After CRC validation on a stable even snapshot, if `state==INVALIDATED` return `ErrInvalidated` (CRC failures stay `ErrBusy`/`ErrCorrupt`).
  - [x] Reserved bytes validation becomes:
    - do NOT require `0x078..0x0BF` to be zero (caller-owned)
    - DO require `0x0C0..0x0FF` to be zero.

---

## Phase 4 — Implement read APIs (user header + generation) ✓

File: `pkg/slotcache/slotcache.go`

- [x] Implement `Cache.UserHeader()`:
  - [x] Use existing seqlock retry pattern (bounded retries + backoff).
  - [x] Under stable `generation`, if `state==INVALIDATED`: return `ErrInvalidated`.
  - [x] Return copied bytes (`[64]byte` copy).

- [x] Implement `Cache.Generation()`:
  - [x] Use seqlock retry pattern (read g1 even → state check → read g2).
  - [x] Return `ErrInvalidated` on invalidated state; `ErrBusy` on repeated odd/changed generation.

---

## Phase 5 — Implement user header write APIs (Writer) ✓

File: `pkg/slotcache/writer.go`

- [x] Add buffered fields to writer:
  - [x] `pendingUserFlags` + `userFlagsDirty`
  - [x] `pendingUserData` + `userDataDirty`

- [x] Implement `Writer.SetUserHeaderFlags` and `Writer.SetUserHeaderData`:
  - [x] Validate closed/invalidated state.
  - [x] Mark only the corresponding field dirty.

- [x] Apply header updates during `Writer.Commit()` publish window:
  - [x] Preserve untouched field:
    - If only flags dirty, keep existing data bytes.
    - If only data dirty, keep existing flags.
  - [x] Ensure preflight failures (`ErrFull`, `ErrOutOfOrderInsert`) do **not** publish any header changes.
  - [x] CRC recompute must include updated header bytes.
  - [x] WritebackSync barriers: header msync already happens; ensure header changes are covered.

---

## Phase 6 — Implement invalidation (terminal state) ✓

File: `pkg/slotcache/slotcache.go` (or a new `invalidate.go`)

- [x] Implement `Cache.Invalidate()`:
  - [x] Fast checks: `ErrClosed`, `ErrBusy` if writer active.
  - [x] Acquire the same exclusivity mechanisms as a writer (in-process guard + optional lock file).
  - [x] Under `registry.mu.Lock()`:
    - publish odd generation
    - set `state=INVALIDATED`
    - recompute header CRC
    - publish even generation
    - msync header barriers in `WritebackSync` mode
  - [x] Decide idempotence: invalidating an already-invalidated file returns nil.

- [x] Make all runtime operations fail fast on invalidation:
  - [x] Read APIs (`Len/Get/Scan/...`) return `ErrInvalidated` under stable snapshot.
  - [x] `BeginWrite` and `Writer.Commit` return `ErrInvalidated`.

Important: invalidation stays **out** of the model-vs-real behavior harness.

---

## Phase 7 — Test infrastructure updates (oracle + fuzz classification) ✓

### Spec oracle
File: `pkg/slotcache/internal/testutil/spec_oracle.go`

- [x] Parse `state` at `0x074`; allow {0,1}, reject others.
- [x] Stop enforcing zero for `0x078..0x0BF` (caller-owned bytes).
- [x] Keep enforcing zero for `0x0C0..0x0FF`.
- [x] Optionally add a helper to assert expected `state` in unit tests.
  - Added `ReadHeaderState()` and `AssertHeaderState()` in `spec_oracle_helpers.go`.

### Fuzz classification
- [x] Update `pkg/slotcache/robustness_fuzz_test.go` to treat `ErrInvalidated` as an allowed `Open()` failure classification.

---

## Phase 8 — Unit tests for header state + user header

Add a new test file (e.g. `pkg/slotcache/header_state_test.go`) and cover:

Invalidation:
- [ ] Invalidate makes current handle unusable (`Len/Get/Scan/BeginWrite` → `ErrInvalidated`).
- [ ] Invalidate observed by another handle to same file.
- [ ] `Open()` on invalidated file returns `ErrInvalidated`.
- [ ] Invalidate idempotence.
- [ ] Invalidate returns `ErrBusy` if writer active.

User header:
- [ ] Defaults to zero on create.
- [ ] Persists across reopen.
- [ ] `Writer.Close()` without commit discards header changes.
- [ ] Commit preflight failure (`ErrFull` / `ErrOutOfOrderInsert`) does not publish header changes.
- [ ] Setting flags does not change data; setting data does not change flags.
- [ ] User header bytes are CRC-protected (manual byte flip on disk → `ErrCorrupt` on next open).

Generation:
- [ ] New file returns generation 0.
- [ ] After commit, generation increases (monotonic change).
- [ ] After invalidation, `Generation()` returns `ErrInvalidated`.

Reserved tail:
- [ ] Non-zero bytes in `0x0C0..0x0FF` cause `Open()` to return `ErrIncompatible`.

---

## Phase 9 — Spec fuzz enhancements (format fuzz)

Files:
- `pkg/slotcache/spec_fuzz_test.go`
- `pkg/slotcache/spec_fuzz_options_test.go`
- `pkg/slotcache/near_cap_fuzz_test.go`

- [ ] Add fuzzer actions that exercise:
  - [ ] `Writer.SetUserHeaderFlags` / `Writer.SetUserHeaderData`
  - [ ] `Cache.Invalidate()` when no writer is active

- [ ] After commits/aborts/invalidation, keep validating on-disk format via `spec_oracle`.
- [ ] After invalidation, reset the run (e.g. delete file + recreate) so fuzz iterations don’t get stuck in terminal `ErrInvalidated` state.

No changes to behavior-model harness unless we explicitly decide to model user header behavior later.

---

## Phase 10 — OrderedKeys scan optimizations (separate track)

(From the approved ordered-scan optimization plan.)

- [ ] Phase 10.1: Add early termination for `Limit` (and `Offset+Limit`) in forward scans.
- [ ] Phase 10.2: Implement reverse-iteration paths (avoid `slices.Reverse`) for ordered mode.
- [ ] Phase 10.3: Ordered-mode order validation during scans (`prevKey <= key`), surfacing `ErrCorrupt`.
- [ ] Phase 10.4: Ordered-mode prefix acceleration (`ScanPrefix`/`ScanMatch`) via binary search range.

- [ ] Tests/benchmarks:
  - [ ] Reverse scans with `Limit/Offset`.
  - [ ] Ordered prefix/range scans match existing results.
  - [ ] Adjust corruption tests if order validation changes expectations.
