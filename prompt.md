# fmcache implementation guide (TDD, observable behavior first)

This repo contains a **spec model** (in-memory reference) and a **test harness** that compares the real implementation against the model.

Your job is to implement the real cache so that the harness passes. **Do not implement mmap optimizations or full format compliance first**; get the **basic observable behavior** correct.

## Goal (Phase 1)

Make the following pass:

- `go test -tags fmcache_impl ./pkg/mddb/fmcache -run Test_Cache_Matches_Spec_When_Operations_Applied`

This is the primary gate. It exercises:

- `Put/Get/Delete` semantics
- ordering (ascending + descending)
- paging (`Offset`, `Limit`) + error behavior
- `FilterEntries` behavior + `AllEntries`
- `Commit` + reopen via `Open(path, ...)`
- closed-cache behavior

## Non-goals (Phase 1)

Do **not** spend time on:

- mmap implementation
- fsync durability modes (`Sync`, `SyncFull`)
- exhaustive binary format validation/corruption handling
- micro-optimizations

Those will be Phase 2+.

## Where to look (important files)

- Spec model (reference behavior): `pkg/mddb/fmcache/fmcache_spec_model.go`
- Harness shared code: `pkg/mddb/fmcache/fmcache_harness_test.go`
- Deterministic op sequence: `pkg/mddb/fmcache/fmcache_property_test.go`
- Public errors: `pkg/mddb/fmcache/errors.go`
- Low-level API stub: `pkg/mddb/fmcache/bytecache.go`
- Typed cache stub: `pkg/mddb/fmcache/fmcache.go`
- Format spec (later): `pkg/mddb/fmcache/spec.md`

## Hard rules (don’t go off-target)

- **Do not change the spec model’s semantics** to make tests pass.
- Prefer adding implementation code in `bytecache.go` / `fmcache.go` rather than changing tests.
- Keep behavior aligned with the spec model:
  - ordering is by key
  - no duplicate keys
  - paging errors must match (`ErrInvalidFilterOpts`, `ErrOffsetOutOfBounds`)
  - closed errors must match (`ErrClosed`)
- Keep code simple and readable. Correctness >>> performance.

## Build/tag workflow

The harness is guarded by the build tag `fmcache_impl`.

During implementation, repeatedly run:

- `go test -tags fmcache_impl ./pkg/mddb/fmcache -run Test_Cache_Matches_Spec_When_Operations_Applied`

You should expect initial panics from `panic("not implemented")`. Replace them incrementally.

## Implementation plan (recommended)

### Step 0: Key validation + shared errors

Implement key validation consistently across both ByteCache and typed Cache:

- invalid if empty
- invalid if contains `\x00`
- later: invalid if too long (when `KeySize` is enforced)

Use `ErrInvalidKey` (and optionally `invalidKey("reason")` for more detail).

### Step 1: Implement ByteCache in the simplest correct way

Implement `ByteCache` in `pkg/mddb/fmcache/bytecache.go` with an in-memory map and a simple persisted snapshot.

Suggested minimal internal state:

- `mem map[string]byteEntry` (the live session)
- `disk map[string]byteEntry` (committed snapshot)

BUT: reopen in the harness happens via `Open(path, ...)` after `Commit()` + `Close()`. That means `Commit()` must actually persist to disk and `OpenByteCache()` must load it.

Recommended approach for Phase 1:

- On `Commit()`: write a whole snapshot file to `path`.
- On `OpenByteCache()`: if file exists, read it fully into memory.

You may implement a very simple on-disk representation first (e.g. gob/json) **only if you keep it contained and easy to replace**; however, it is usually better to already write the FMC1-ish snapshot structure but without mmap.

**Do not implement incremental updates**; rewriting the whole snapshot is fine.

Required ByteCache methods for the harness path (directly or indirectly):

- `Len()`
- `Get()`
- `Put()`
- `Delete()`
- `Commit()`
- `FilterIndex()` and/or `AllEntries()` (depending on how you implement typed filtering)

### Step 2: Implement typed Cache[T]

Implement `Cache[T]` in `pkg/mddb/fmcache/fmcache.go` as a thin wrapper around `ByteCache`:

- `Put(key, rev, value)`:
  - validate key
  - `schema.Encode(&value)` -> `(indexBytes, dataBytes)`
  - call `bc.Put(key, rev, indexBytes, dataBytes)`
- `Get(key)`:
  - validate key
  - call `bc.Get(key)`
  - `schema.Decode(entry.Index, entry.Data)`
- `FilterEntries(opts, match)`:
  - must return entries in key order and apply paging
  - Phase 1 simplest: call `AllEntries(opts)` and filter in Go by decoding values
  - later: switch to index-only
- `Commit()`:
  - call `bc.Commit()`
- `Close()`:
  - set closed and close underlying `bc`

### Step 3: Make `CommitReopenOp` pass

The harness does:

- `Commit()`
- `Close()`
- `Open(path, schema)`

So `Open()` must reflect what was committed.

If you do not persist anything to disk, the harness will diverge from the spec model.

### Step 4: Filtering + paging correctness

Match spec-model behavior:

- negative `Offset` or `Limit` => `ErrInvalidFilterOpts`
- `Offset > matchCount` => `ErrOffsetOutOfBounds`
- key order is the only guaranteed order
- `Reverse` means descending key order

### Step 5: Closed cache behavior

After `Close()` all methods must return `ErrClosed` (or in the typed wrapper: behave as spec model does).

## Definition of done (Phase 1)

- `go test -tags fmcache_impl ./pkg/mddb/fmcache -run Test_Cache_Matches_Spec_When_Operations_Applied` passes
- Code is straightforward, no clever optimizations
- No changes to spec model semantics

## Phase 2+ (later)

After Phase 1 passes, we’ll add dedicated format tests for `spec.md` (header fields, reserved bytes, corruption detection), and then optimize the read path (mmap, index-only filtering, etc.).
