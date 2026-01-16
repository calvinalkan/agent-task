# slotcache Implementation Plan (SLC1)

## Current State (confirmed)

- `pkg/slotcache/slotcache.go` + `pkg/slotcache/writer.go` implement a **gob-backed stub** (not the SLC1 binary format).
- `make test` currently fails in `pkg/slotcache`:
  - `FuzzSpec_GenerativeUsage/44fcc42928bd2dba`: `speccheck: file too small: got 237 bytes, need at least 256`
- `pkg/slotcache/format.go` already contains **spec-level primitives**:
  - header offsets/constants
  - header CRC32-C computation (with generation+crc bytes zeroed)
  - slot encode/decode + slot sizing
  - bucket_count sizing helpers
- `pkg/slotcache/writer_lock.go` already implements the **lock file + non-blocking flock** behavior and returns `ErrBusy` on contention.

Goal: replace the stub with a real mmap-backed implementation that produces spec-compliant SLC1 files and matches the public API behavioral model.

---

## Prioritized Work (TODO)

### P0 — Must: make `make test` pass (spec-oracle regression + model parity)

1. **Wire up the OrderedKeys header flag**
   - [ ] Add `slc1FlagOrderedKeys uint32 = 1 << 0` in `pkg/slotcache/format.go`.
   - [ ] Update `newHeader(...)` to accept `orderedKeys bool` and set `Flags` accordingly.
   - [ ] Update `format_test.go` call sites for the new signature.

2. **Introduce a real SLC1 file abstraction (fd + mmap + accessors)**
   - [ ] Create an internal type (e.g. `slcFile`) that owns:
     - an `*os.File` opened `O_RDWR`
     - a `[]byte` mapping created with `mmap(MAP_SHARED)`
     - cached immutable config fields: keySize, indexSize, slotSize, slotsOffset, bucketsOffset, bucketCount, flags
   - [ ] Add `mmap` / `munmap` helpers using `syscall.Mmap` / `syscall.Munmap` (MAP_SHARED).
   - [ ] Decide/document the in-process safety strategy:
     - **Option A:** per-file `sync.RWMutex` registry to keep Go race-detector clean (serializes in-process reads vs writes), while still using seqlock for cross-process.
     - **Option B:** atomic word-level access for all mutable fields (generation + counters + bucket words + slot meta/revision/index words) to keep reads lock-free *and* race-detector-friendly.

3. **Implement `Open()` per spec (create/init/validate + mmap)**
   - [ ] Validate Options:
     - `Path` non-empty
     - `KeySize >= 1`, `IndexSize >= 0`, `SlotCapacity >= 1`
     - `SlotCapacity <= 0xFFFFFFFFFFFFFFFE`
   - [ ] Handle file states:
     - doesn’t exist: create temp file in same dir (mode `0600`), `ftruncate` to full size, write header, rename
     - exists, 0 bytes: initialize **in place** preserving permissions/ownership
     - exists, 1–255 bytes: `ErrCorrupt`
     - exists, ≥256 bytes: read + validate header + derived layout + invariants; mismatch → `ErrIncompatible`
   - [ ] Header validation requirements (minimum):
     - magic/version/header_size/hash_alg
     - unknown flags → `ErrIncompatible`
     - reserved_u32/reserved bytes non-zero → `ErrIncompatible`
     - header CRC mismatch → `ErrCorrupt`
     - config mismatch (key_size/index_size/user_version/slot_capacity/slot_size) → `ErrIncompatible`
     - structural invariants (`bucket_used == live_count`, offsets, file size floor, etc.) → `ErrCorrupt`
   - [ ] Locking interaction for odd generation:
     - locking enabled: briefly acquire writer lock during Open; if generation remains odd while we hold lock → crashed writer → `ErrCorrupt`
     - locking disabled: odd generation → `ErrBusy`
   - [ ] `mmap` the file using `MAP_SHARED` and return a Cache handle.

4. **Implement in-process single-writer enforcement keyed by file identity (dev+inode)**
   - [ ] Replace the current path-keyed `fileRegistry` / `writerActive` with a package-global registry keyed by `(dev, ino)`.
   - [ ] Ensure `BeginWrite()` is safe under concurrent goroutines and across multiple Cache instances.
   - [ ] This is **required** because `flock` is per-process: without an in-process registry, multiple writers in one process can succeed.

5. **Implement bucket index (hash table) per spec**
   - [ ] Add FNV-1a 64-bit hash (same constants as `internal/testutil/spec_oracle.go`).
   - [ ] Implement bucket encoding helpers:
     - EMPTY: `slot_plus1 == 0`
     - TOMBSTONE: `slot_plus1 == ^uint64(0)`
     - FULL: `slot_id = slot_plus1 - 1`, `hash64` must match `hash(key)`
   - [ ] Implement bounded linear-probing lookup:
     - start `i = hash & (bucket_count-1)`
     - probe `i = (i+1) & mask`
     - stop at EMPTY; continue past tombstones + hash collisions
     - verify slot_id < slot_highwater under stable generation (else corruption)
     - verify slot key bytes equality before returning a match
   - [ ] Implement insert + delete:
     - insert into EMPTY or TOMBSTONE
     - delete sets `slot_plus1` to TOMBSTONE
     - maintain `bucket_used` and `bucket_tombstones`
     - enforce invariant `bucket_used + bucket_tombstones < bucket_count`

6. **Rewrite Writer to commit into SLC1 slots + buckets**
   - [ ] Keep buffered-op semantics: last op per key wins, preserving input order (to match the model).
   - [ ] At commit time, compute the effective delta vs the committed on-disk state:
     - update (existing live key + Put)
     - insert (absent key + Put)
     - delete (existing live key + Delete)
     - no-op (absent key + Delete)
     - ensure `Put(new); Delete(new)` in the same session does **not** allocate a slot (metamorphic law)
   - [ ] **Preflight checks must happen before any publish / file mutation**:
     - capacity check (`slot_highwater + newInserts <= slot_capacity`) else `ErrFull` and no changes
     - ordered-mode check (`minNewKey >= tailKey` where tailKey is slot `slot_highwater-1` key bytes, even if tombstoned) else `ErrOutOfOrderInsert` and no changes
   - [ ] Apply under writer lock:
     - publish generation odd
     - apply updates/deletes/inserts (ordered mode: inserts sorted and appended)
     - maintain header counters: slot_highwater, live_count, bucket_used, bucket_tombstones
     - recompute + store header CRC
     - publish generation even
   - [ ] Ensure every successful commit produces a file that passes `internal/testutil.ValidateFile`.

7. **Rewrite Cache read paths to use SLC1 structures**
   - [ ] `Len()` returns header `live_count` (with coherence checks/retries when generation changes).
   - [ ] `Get()` uses bucket lookup and returns copies of key/index bytes.
   - [ ] `Scan`/`ScanPrefix`/`ScanMatch` scan slot IDs, skip tombstones, apply filter+offset+limit, yield via Cursor.
   - [ ] `ScanRange`:
     - return `ErrUnordered` if ordered flag is not set
     - validate/pad bounds per spec
     - correctness first: sequential scan + comparison is acceptable initially (binary search can be deferred)
   - [ ] Cursor semantics:
     - if generation changes mid-iteration, stop early and set `Cursor.err = ErrBusy`.

8. **Close semantics / lifecycle correctness**
   - [ ] Cache.Close idempotent: `munmap` + close fd; return `ErrBusy` if a writer from this cache handle is still active.
   - [ ] After Close, all other Cache methods return `ErrClosed`.
   - [ ] Writer.Close idempotent; after Commit or Close, further Writer ops return `ErrClosed`.

---

### P1 — Spec completeness (durability + crash semantics)

- [ ] Implement `WritebackMode`:
  - `WritebackNone`: no `msync`
  - `WritebackSync`: `msync` barriers per spec (header odd → data → header even)
  - ensure `msync` ranges are page-aligned (macOS requirement)
  - if any `msync` fails: still complete commit and return `ErrWriteback`
- [ ] Implement tombstone-driven rehashing (e.g. when `bucket_tombstones/bucket_count > 0.25`) during Commit.
- [ ] Implement bounded point-read retries with backoff; return `ErrBusy` after exhausting retries.

---

### P2 — Performance and hardening

- [ ] Ordered range scan optimization: binary search to find start slot, then sequential scan.
- [ ] Optional extra corruption detection:
  - sample-check buckets at Open
  - stricter invariant checks during reads (return `ErrCorrupt` under stable even generation)
- [ ] Allocation reductions in scans (without violating “returned slices are caller-owned”).

---

## Validation / Done Criteria

- [ ] `make test` passes (includes the fuzz regression `FuzzSpec_GenerativeUsage/*`).
- [ ] `make lint` passes.
- [ ] `go test ./pkg/slotcache -fuzz=FuzzSpec_GenerativeUsage -fuzztime=30s` runs without failures.
- [ ] `go test ./pkg/slotcache -fuzz=FuzzSpec_OpenAndReadRobustness -fuzztime=30s` runs without panics/hangs.
