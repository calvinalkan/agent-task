# Semantics

This document defines the behavioral semantics of slotcache: concurrency model, operations, validation, and error conditions.

---

## Concepts

**Terminology note:** This spec uses "tombstone" in two contexts:
- **Slot tombstone**: a slot with `USED=0`; the slot remains allocated and (in ordered mode) preserves its key bytes
- **Bucket tombstone**: a hash table entry with `slot_plus1=0xFFFFFFFFFFFFFFFF`; needed for correct linear probing

Context usually makes the meaning clear.

### Key

- A **key** is a fixed-size byte string of length `key_size`
- slotcache treats keys as **opaque bytes** (no parsing / no validation beyond length)

**Design intent (not enforced):** keys are often chosen so that lexicographic order correlates with time (e.g., UUIDv7/ULID), which enables fast ordered scans when ordered-keys mode is enabled.

### Slot

A **slot** is a fixed-size record stored in the slots section.

Each slot contains:

- `meta` (internal flags; includes live/tombstone state)
- `key` (fixed-size bytes)
- `revision` (opaque `int64` provided by caller; typically source-of-truth mtime/generation)
- `index` (fixed-size opaque bytes for filtering)

Slot IDs are **append-only** in v1:

- Slot IDs are assigned monotonically from `0..(slot_highwater-1)`
- Slot IDs are never reused
- Deleting a key marks its slot as a tombstone (does not reclaim slot ID)
- `Put` for an existing key MUST update the existing slot in place and MUST NOT allocate a new slot ID

### Index bytes

`index` is a fixed-size byte array per slot (`index_size` bytes).

- slotcache does not interpret or validate these bytes
- Higher layers compile schemas (enums, numbers, padded strings, bitsets, etc.) into this byte layout

### Revision

`revision` is an `int64` stored verbatim. It is **not interpreted** by slotcache.

Typical usage: store source file mtime (ns) or a logical generation number to detect staleness without re-reading every file.

### Hash index (buckets)

slotcache maintains an on-disk hash table mapping `key → slot_id` for fast point lookups.

- It is an **open-addressed hash table** with linear probing
- Buckets store `(hash64(key), slot_id+1)`
- Deletes create hash-table tombstones

### Ordered-keys mode (optional)

slotcache normally makes **no ordering promises** about slot order. Scans are "scan-fast" but O(n).

If (and only if) the cache file is created with **ordered-keys mode** enabled (`FLAG_ORDERED_KEYS`), then:

- The slots array is **globally sorted by key bytes** in slot-id order
- The writer MUST enforce **append-only ordered inserts** (append-or-error)
- Deleted/tombstoned slots MUST preserve their key bytes so the slots array remains globally sorted
- The implementation MAY provide ordered scan APIs that use **binary search + sequential scan** internally

Ordered-keys mode does **not** require shifting records on insert. It forbids "insert into the middle" and instead uses:

- **append if in-order**, otherwise
- return an error (caller rebuilds the cache in-order)

This matches slotcache's "throwaway cache" model.

---

## Concurrency model

- **Multi-reader, single-writer**
- Readers are lock-free (they do not take interprocess locks)
- Writers serialize via an interprocess writer lock (optional but recommended) and an in-file seqlock

### Writer lock

If locking is enabled (recommended), the implementation uses an advisory lock (e.g., `flock`) on a separate lock file.

- There MUST NOT be more than one active writer at a time
- If locking is disabled, callers MUST enforce single-writer externally; slotcache assumes equivalent synchronization exists around all cache access

**Why allow disabling locks?** slotcache may be embedded inside another single-writer component (e.g., a database engine) that already coordinates access via its own locking. In this case, slotcache's lock would be redundant. Disabling it avoids double-locking overhead. If callers disable locking without providing equivalent external synchronization, behavior is undefined.

Odd `generation` values have two possible meanings:

- an active writer is mid-commit (and, if locking is enabled, holds the writer lock), **or**
- a previous writer crashed mid-commit and left the file in an unpublished state.

If locking is enabled and `Open` is able to acquire the writer lock, then observing an odd `generation` indicates the second case (crashed writer) and the file MUST be treated as `ErrCorrupt` (caller recreates).

If locking is disabled, slotcache cannot distinguish these cases on its own. In that configuration, read operations SHOULD treat an odd `generation` as `ErrBusy`. Callers that can guarantee exclusive access (because they hold an external writer lock) MAY treat an odd `generation` as corruption and recreate.

A Cache MUST NOT have more than one active Writer at a time. If `BeginWrite` is called while a Writer is already active—whether in the same process or another process—it MUST return `ErrBusy`.

### Thread safety

**Read operations** (Get, Scan, iteration) on a Cache MUST be safe for concurrent use by multiple threads.

**Acquiring a Writer** (BeginWrite) MUST be safe to call concurrently from multiple threads, whether on a single Cache instance or across multiple instances backed by the same file. At most one call succeeds; others MUST return `ErrBusy`. File locks do not prevent same-process conflicts, so implementations MUST use a package-global mutex-protected registry keyed by file identity (device:inode pair) to coordinate writers within a process.

**Using a Writer** (Put, Delete, Commit, Close) is NOT required to be thread-safe. Callers MUST synchronize access to a Writer externally if sharing across threads.

**Multiple Cache instances:** Opening multiple Cache instances on the same file within one process is permitted for reading.

**Reading while writing:** Read operations MAY be called on a Cache while a Writer is active (on the same or different Cache instance). Readers observe the last committed state; buffered writes are not visible until Commit completes.

### Seqlock generation counter

The header field `generation` is a seqlock counter:

- **even** = stable snapshot
- **odd** = writer in progress

Requirements:

- `generation` MUST be a monotonically increasing 64-bit counter
- Writers MUST NOT publish by only toggling the low bit
- `generation` MUST be 8-byte aligned and updated using atomic 64-bit ops
- Atomic operations MUST provide acquire/release (or stronger) ordering such that:
  - a reader that observes a stable even `generation` also observes all slot/bucket/header writes that happened-before that publication
  - a writer does not reorder data writes after the final even publication

Platforms that cannot perform aligned atomic 64-bit operations across processes are out of scope for v1.

### Reader coherence rule

All read operations MUST be **correct-or-retry** under the seqlock:

1. Read `g1 = generation` atomically
2. If `g1` is odd: retry; if it remains odd, return `ErrBusy`
3. Perform the read (scan / hash lookup) using mmapped structures
4. If an impossible invariant is observed during the read, immediately re-read `gX = generation` atomically:
   - if `gX` is odd OR `gX != g1`: treat as overlap and retry
   - else (stable even): return `ErrCorrupt`
5. Read `g2 = generation` atomically; if `g1 != g2`, retry; else return results

**Retry requirements:**

- Reads MUST NOT retry forever; implementations MUST eventually return `ErrBusy`
- Implementations SHOULD use backoff between retries to avoid busy-spinning
- The specific retry count, backoff strategy, and timeout are implementation-defined

---

## Write session lifecycle

Writes are performed via a buffered writer session.

### BeginWrite

Starts a write session:

- If locking is enabled, acquires the writer lock (non-blocking)
- Creates an in-memory buffer for operations
- No on-disk state is modified yet

**Errors:** `ErrClosed`, `ErrBusy` (lock contention or writer already active)

### Put / Delete

Buffer operations in memory:

- `Put(key, revision, index)` stages an upsert
- `Delete(key)` stages a deletion
- Within a session, **last operation wins** per key
- The writer lock remains held; the file stays at a stable even generation

**Operation coalescing:** At commit time, the writer computes the effective delta per key by comparing the net buffered operation against the on-disk state at BeginWrite time:

| On-disk state | Net buffered op | Effective action |
|---------------|-----------------|------------------|
| Key exists | Put | Update (reuse existing slot) |
| Key exists | Delete | Delete (tombstone slot) |
| Key absent | Put | Insert (allocate new slot) |
| Key absent | Delete | No-op |

This means `Delete("A")` followed by `Put("A")` in the same session coalesces to an **Update** (not a new insert), which reuses the existing slot and does not trigger ordered-keys validation.

### Commit

Publishes buffered changes.

**WritebackMode = None:**

1. Set `generation` to a new odd value
2. Apply buffered ops to slots, buckets, and header counters
3. Recompute and store the header CRC
4. Set `generation` to a new even value
5. Release the writer lock
6. Session ends

No `msync` calls; changes are visible to other processes via `MAP_SHARED` but have no durability guarantees.

**WritebackMode = Sync:**

1. Set `generation` to a new odd value
2. `msync` the header (barrier: ensures odd is on disk before data modifications)
3. Apply buffered ops to slots, buckets, and header counters
4. Recompute and store the header CRC
5. `msync` all modified data (barrier: ensures data is on disk)
6. Set `generation` to a new even value
7. `msync` the header (barrier: ensures even is on disk)
8. Release the writer lock
9. Session ends

**After Commit (either mode):** further operations on this Writer MUST return `ErrClosed`.

If a crash occurs mid-commit, readers MUST detect it (odd generation or validation failure) and the caller rebuilds.

**msync failure handling:** If any `msync` call fails during Sync mode, the implementation MUST still complete the commit sequence (including setting generation to even). The data is visible to other processes via `MAP_SHARED`; only durability is compromised. Return `ErrWriteback` to indicate the durability guarantee was not achieved. Callers that require durability SHOULD recreate the cache.

**Errors:** `ErrClosed`, `ErrFull` (slot capacity exhausted), `ErrOutOfOrderInsert` (ordered mode, key < tail), `ErrWriteback` (msync failed but commit completed)

### Close

Releases resources and the writer lock:

- If `Commit` was not called, buffered changes are discarded
- `Close` is idempotent; subsequent calls are no-ops
- After `Close`, further operations on this Writer MUST return `ErrClosed`

To publish changes, call `Commit`. To discard changes, call `Close` without calling `Commit`.

---

## Writeback semantics

Writeback controls flushing of dirty mmapped pages to the backing file.

Writeback is **not** required for interprocess visibility (`MAP_SHARED` already provides that); it primarily affects:

- Durability across crash/power loss
- Dirty memory pressure
- Surfacing delayed allocation errors closer to `Commit`

### Writeback modes

- **None**: No `msync` calls; rely on kernel writeback; no durability guarantees
- **Sync**: Full barrier sequence with `msync(MS_SYNC)`; power-loss safe

How the caller selects a writeback mode is implementation-defined (typically configured at Open time).

### Power-loss safety (Sync mode)

The Sync barrier sequence ensures that after power loss, the cache is in one of two states:

1. **Previous state**: generation is even, data is from before the interrupted commit
2. **Detected as incomplete**: generation is odd, triggers `ErrCorrupt` on next `Open`

The key insight: `msync` after setting generation=odd ensures the "write in progress" marker is on disk **before** any data modifications can reach disk. This prevents the "silent corruption" case where data is partially written but generation appears clean.

Because this is a throwaway cache, callers typically delete and recreate on writeback errors.

---

## Open / Close

### Lock file conventions

When interprocess locking is enabled, slotcache uses an advisory lock file to coordinate writers.

**Lock file path:** `{cache_path}.lock` (e.g., `/var/cache/foo.cache` → `/var/cache/foo.cache.lock`)

**Lock file permissions:** `0600` (same as cache file)

**Lock file lifecycle:**
- Created when first needed (during `Open` with locking enabled)
- Persists after close (not deleted)
- Uses `flock()` for advisory locking

### File permissions

**New cache file:** `0600` (owner read/write only)

**Pre-created empty file:** Permissions are preserved (see "0-byte file" below)

**Lock file:** `0600`

### Open

Opens or creates a cache file. `Open` always returns a read-only `Cache` handle; use `BeginWrite` to acquire write access.

#### Open invariants

`Open` MUST guarantee:

1. **No partial files:** If creation fails, no file (or only an empty file) remains at the target path
2. **No silent races (with locking):** When locking is enabled, concurrent creates are serialized
3. **Consistent state:** The returned `Cache` observes a valid, stable-generation snapshot

#### File state handling

| File state | Behavior |
|------------|----------|
| Doesn't exist | Create new file (0600, temp + rename) |
| Exists, 0 bytes | Initialize in place (preserve permissions) |
| Exists, 1–255 bytes | Return `ErrCorrupt` |
| Exists, ≥256 bytes, bad magic | Return `ErrIncompatible` |
| Exists, ≥256 bytes, wrong version | Return `ErrIncompatible` |
| Exists, ≥256 bytes, config mismatch | Return `ErrIncompatible` |
| Exists, ≥256 bytes, valid | Validate and open |

**0-byte file use case:** Allows administrators to pre-create files with specific permissions/ownership before the application runs.

```bash
# Admin pre-creates with desired permissions
touch /var/cache/app/data.cache
chown appuser:appgroup /var/cache/app/data.cache
chmod 0640 /var/cache/app/data.cache

# Application initializes the empty file, preserving 0640
```

#### Open flow (with locking enabled)

```
Open(path, opts) [locking=ON]
│
▼
┌───────────────────────────────────┐
│ Create lock file (O_CREAT|O_EXCL) │
│ or open existing lock file        │
└───────────────────────────────────┘
         │
    ┌────┴────────┐
    │ Created     │ Already exists
    ▼             ▼
┌─────────────────────────────────────┐
│ Acquire flock()                     │
│ (block or EAGAIN based on opts)     │
└─────────────────────────────────────┘
              │
         ┌────┴────┐
         │ Failed  │ Acquired
         ▼         │
     ErrBusy       │
     (another      │
     writer)       │
                   │
                   ▼
    ┌───────────────────────────────────┐
    │ fd = open(cache_path, O_RDWR)     │
    └───────────────────────────────────┘
              │
         ┌────┴────┐
         │ ENOENT  │ Success
         ▼         ▼
    ┌─────────┐  ┌─────────────────────┐
    │ CREATE  │  │ fstat(fd) → size    │
    │ (temp + │  └─────────────────────┘
    │ rename) │           │
    └─────────┘      ┌────┼────────┬────────────┐
         │           │    │        │            │
         │           ▼    ▼        ▼            ▼
         │        0 bytes 1–255   ≥256         ≥256
         │           │    │        │            │
         │           ▼    ▼        ▼            ▼
         │        INIT  ErrCor-  Validate    Validate
         │        IN    rupt     header      header
         │        PLACE          ↓            ↓
         │           │        Invalid?     Valid
         │           │           │            │
         │           │           ▼            │
         │           │     ErrIncompatible    │
         │           │     or ErrCorrupt      │
         └───────────┴───────────┬────────────┘
                                 │
                                 ▼
                   ┌─────────────────────────────┐
                   │ Check generation            │
                   └─────────────────────────────┘
                              │
                         ┌────┴────┐
                         │ Odd     │ Even
                         ▼         ▼
              ┌──────────────────┐ │
              │ Re-read generation│ │
              │ (we hold lock)    │ │
              └──────────────────┘ │
                      │            │
                 ┌────┴────┐       │
                 │ Odd     │ Even  │
                 ▼         ▼       │
            ┌────────┐     │       │
            │Release │     │       │
            │lock,   │     │       │
            │ErrCor- │     │       │
            │rupt    │     │       │
            └────────┘     │       │
                           └───┬───┘
                               │
                               ▼
                   ┌─────────────────────────────┐
                   │ Release lock                │
                   │ mmap file                   │
                   │ Return Cache (reader)       │
                   └─────────────────────────────┘
```

#### Open flow (locking disabled)

```
Open(path, opts) [locking=OFF]
│
▼
┌───────────────────────────────────┐
│ fd = open(cache_path, O_RDWR)     │
└───────────────────────────────────┘
         │
    ┌────┴────┐
    │ ENOENT  │ Success
    ▼         ▼
 CREATE    fstat → size
 (temp +        │
 rename,   ┌────┴────┬────────────┐
 RACY)     │         │            │
           ▼         ▼            ▼
        0 bytes   1–255        ≥256
           │         │            │
           ▼         ▼            ▼
        INIT     ErrCorrupt   Validate...
        (RACY)                    │
           │                 ┌────┴────┐
           │                 │ Odd     │ Even
           │                 ▼         ▼
           │             ErrBusy    SUCCESS
           │             (caller's
           │             external lock
           │             implies active
           │             writer)
           └──────────────────┴───────────────┐
                                              ▼
                                          SUCCESS
```

**Note:** With locking disabled, the caller MUST provide equivalent external synchronization. Concurrent creates race (last writer wins) if callers violate this requirement.

#### Create: temp + rename

When creating a new file (file doesn't exist):

1. Create temporary file in same directory: `{path}.tmp.{random}` with mode `0600`
2. `ftruncate` to full size (creates sparse file)
3. Write and sync header
4. `rename(temp, target)` — atomic on POSIX

If any step fails, remove the temp file. This ensures the target path never contains a partial file.

#### Create: initialize in place

When initializing a 0-byte file:

1. Open existing file `O_RDWR` (preserves permissions)
2. `ftruncate` to full size
3. Write header
4. Sync

This preserves the file's existing permissions and ownership.

**Errors:**
- `ErrCorrupt` — file is truncated (1–255 bytes), header CRC mismatch, or structural invariant violation
- `ErrIncompatible` — wrong magic/version, unknown flags, config mismatch (key_size, index_size, user_version, slot_capacity)
- `ErrBusy` — writer is active (lock contention), or the file is in an in-progress generation and the implementation cannot prove exclusivity (e.g., locking disabled)

### Close

Closes the cache:

- Unmaps the file
- Releases any held resources
- After `Close`, operations on this Cache MUST return `ErrClosed`

---

## Required operations

Implementations MUST provide the following operations:

**Lifecycle:**
- Open (create or open existing cache file)
- Close (release resources)

**Read operations:**
- Get (point lookup by exact key)
- Scan (iterate live slots with caller-provided predicate)
- Prefix match (scan with bit/byte prefix filter; see Prefix matching)
- Len (return count of live entries)

**Write operations:**
- BeginWrite (start write session, acquire lock)
- Put (stage upsert)
- Delete (stage deletion)
- Commit (publish buffered changes)
- Close (release writer, discard uncommitted changes)

**Ordered mode only (FLAG_ORDERED_KEYS):**
- Range iteration (ordered cursor with start/end bounds)

Exact function signatures and return types are implementation-defined.

---

## Read operations

### Byte ownership rules

- Read APIs that return results MUST return **copied** key bytes and **copied** index bytes so callers can retain results safely
- Callback/predicate APIs MAY pass **borrowed** slices that are valid only for the duration of the callback

### Scan consistency modes (streaming vs snapshot)

All scan-style operations (Scan, ScanMatch, ScanPrefix, ScanRange) MUST return entries from a single stable even generation. Implementations MAY choose either of these strategies:

**Snapshot mode:**
- Acquire a stable generation using the reader coherence rule (`g1`/scan/`g2`) over the entire scan
- If the generation changes or is observed odd before completion (after bounded retries), the operation MUST return `ErrBusy` and MUST NOT return any results
- The returned cursor iterates over copied results and cannot fail mid-iteration

**Streaming mode:**
- Read `g1` once (must be even) and scan incrementally
- Before yielding each entry, the implementation MUST verify the generation is still even and equal to `g1`
- If the generation changes or becomes odd, iteration MUST stop and `ErrBusy` MUST be reported
- Partial results MAY have been yielded, but all yielded entries are guaranteed to come from generation `g1`

### Len

`Len()` returns the number of live entries in the cache (i.e., `live_count` from the header). Tombstoned slots are not counted.

**Errors:** `ErrClosed`, `ErrBusy`, `ErrCorrupt`

### Get (exact lookup)

`Get(key)` is a point lookup:

- Validates key length equals `key_size`
- Uses the hash index to find the bucket, then verifies slot key equality
- On miss, returns `(zeroValue, false, nil)`

**Errors:** `ErrClosed`, `ErrBusy`, `ErrCorrupt`, `ErrInvalidInput` (key wrong length)

### Scan (filtering)

Slot scans iterate slot IDs sequentially and evaluate predicates on live slots.

- Scans are ordered by **slot ID** (ascending by default)
- Tombstones are skipped
- Only slots matching the caller-provided predicate are returned

Consistency and `ErrBusy` behavior follow **Scan consistency modes (streaming vs snapshot)**.

**Filter options:**

- `Reverse`: if true, scan in descending slot ID order
- `Offset`: number of matching results to skip before returning
- `Limit`: maximum number of results to return (0 = no limit)

**Offset out of bounds:** If `Offset` exceeds the number of matching slots, implementations MAY either return an empty result set (clamping) or return an error. Either behavior is acceptable.

**Errors:** `ErrClosed`, `ErrBusy`, `ErrCorrupt`

### Prefix matching

slotcache supports a general "prefix match" primitive intended for disambiguation UIs (e.g., human/agent short-ID matching).

**Why bit-granularity?** Human-readable short IDs are often derived from encodings like base32 (5 bits per character). A 3-character prefix represents 15 bits, which doesn't align to byte boundaries.

**UUIDv7 example:** Keys are 16-byte UUIDv7s. The first 6 bytes are timestamp; the remaining bytes contain random bits. To generate human-friendly short IDs, you encode the random portion as base32 (e.g., `"k7x"`). When a user types `"k7"` to disambiguate:
- `KeyOffset = 6` (skip timestamp bytes)
- `PrefixBits = 10` (2 base32 chars × 5 bits)
- `prefixBytes` = the 10-bit pattern decoded from `"k7"`, packed into 2 bytes

A **match spec** is defined by:

- `KeyOffset`: byte offset within key to start matching
- `PrefixBits`: number of bits to match (0 means "byte-aligned length derived from prefixBytes")
- `prefixBytes`: bytes containing the match pattern

Matching rules:

- Compare bits MSB-first starting at `key[KeyOffset]`
- If `PrefixBits == 0`:
  - Perform a byte-aligned match of exactly `len(prefixBytes) * 8` bits
  - `len(prefixBytes)` MUST be in `1..(key_size - KeyOffset)`
- If `PrefixBits > 0`:
  - Let `needBytes = ceil(PrefixBits / 8)`
  - `len(prefixBytes)` MUST equal `needBytes`
  - Bits beyond `PrefixBits` in the final byte MUST be ignored

**Bit-level example:** With `PrefixBits=10` and `prefixBytes=[0xAB, 0xC0]`:
- We match 10 bits: all 8 bits of byte 0 (`0xAB`) plus the top 2 bits of byte 1 (`0xC0 = 0b11000000`)
- The pattern is `0xAB` followed by `0b11xxxxxx` (where `x` = don't care)
- Keys matching: `[0xAB, 0xC0]`, `[0xAB, 0xC7]`, `[0xAB, 0xFF]`, ...
- Keys not matching: `[0xAB, 0x80]` (top 2 bits are `0b10`, not `0b11`)

This primitive is **not** accelerated by the hash index (hashing destroys ordering information). It is implemented as an O(n) scan.

Consistency and `ErrBusy` behavior follow **Scan consistency modes (streaming vs snapshot)**.

**Prefix validation:**

The following are invalid:

- `prefixBytes` is empty and `PrefixBits == 0`
- `KeyOffset >= key_size`
- `KeyOffset + len(prefixBytes) > key_size` (when `PrefixBits == 0`)
- `KeyOffset + ceil(PrefixBits / 8) > key_size` (when `PrefixBits > 0`)

**Errors:** `ErrClosed`, `ErrBusy`, `ErrCorrupt`, `ErrInvalidInput` (invalid prefix spec)

### Ordered range iteration

If and only if `FLAG_ORDERED_KEYS` is set, the implementation provides ordered range iteration.

- Results are produced in **key order** (which equals slot-id order) or reverse key order
- The iteration considers all allocated slots `0..slot_highwater-1`, skipping tombstones

**Bounds:**

- `start` and `end` define a half-open range: `start <= key < end`
- Either bound may be nil (unbounded)
- Non-nil bounds MUST have length `1..key_size`
- Bounds shorter than `key_size` are right-padded with `\x00` bytes internally for comparison
- If `start` and `end` are both non-nil and `start > end` lexicographically, the range is invalid

**Implementation (non-normative):** the iterator uses binary search over the slots array to find the first slot whose key is `>= start`, followed by a sequential scan (skipping tombstones) until keys are `>= end`.

Binary search works correctly because tombstoned slots preserve their key bytes (required by ordered-keys mode). The binary search may land on a tombstone; the sequential scan skips it. Only live slots are returned to the caller.

Consistency and `ErrBusy` behavior follow **Scan consistency modes (streaming vs snapshot)**. In snapshot mode, the range is captured before any results are returned; in streaming mode, a generation change stops iteration with `ErrBusy`.

**Errors:** `ErrClosed`, `ErrBusy`, `ErrCorrupt`, `ErrInvalidInput` (invalid bounds), `ErrUnordered` (file not created with ordered mode)

---

## Write operations

### Put

`Put(key, revision, index)` stages an upsert:

- Validates key length equals `key_size`
- Validates index length equals `index_size`
- If key exists: update will modify existing slot in place
- If key is new: a new slot will be allocated at commit time

**Errors:** `ErrClosed`, `ErrInvalidInput` (key or index wrong length)

### Delete

`Delete(key)` stages a deletion:

- Validates key length equals `key_size`
- If key doesn't exist, the delete is a no-op
- Implementations SHOULD return whether the key existed (considering on-disk state at BeginWrite plus buffered ops so far)

**Errors:** `ErrClosed`, `ErrInvalidInput` (key wrong length)

### Delete and the hash index

Even with slot tombstones (`meta` USED=0), the hash index entry MUST be removed (converted to a hash-table TOMBSTONE):

- If you keep `key → slot_id` mappings for deleted slots, the hash table never frees capacity
- Reinserting a previously deleted key would be ambiguous
- Therefore `Delete` MUST update the hash index and MUST mark the slot as deleted

### Ordered inserts enforcement

If `FLAG_ORDERED_KEYS` is set, then at every published generation:

- For all allocated slot IDs `i < j < slot_highwater`, `slot[i].key <= slot[j].key` (lexicographic byte order)

Writer requirements:

- New inserts MUST be appended in non-decreasing key order
- The writer MUST reject any commit that would append a new key smaller than the key stored in slot `slot_highwater-1` (the last allocated slot), even if that last slot is tombstoned
- Deletes do not reduce `slot_highwater` and therefore do not relax the monotonic insert requirement

**Commit-time algorithm (normative constraints, not a mandated implementation):**

1. Compute the effective operation set (last-wins per key)
2. Identify which effective `Put`s are **new inserts** (key not present at begin-write and not created earlier in the same transaction)
3. Sort those new keys by lexicographic order
4. If there is at least one new key and `slot_highwater > 0`, require `minNewKey >= tailKey` where `tailKey` is the key bytes of slot `slot_highwater-1`
5. Apply new inserts in sorted order

If the commit violates ordering, it MUST fail with `ErrOutOfOrderInsert` and MUST NOT publish.

**Example (ordered mode):**

After inserting "bbb" (slot 0) and "ccc" (slot 1), then deleting "bbb":

- Slot 0 is a tombstone with key "bbb" preserved
- Slot 1 is live with key "ccc"
- `slot_highwater` = 2

Inserting "aaa" fails with `ErrOutOfOrderInsert` because "aaa" < "ccc" (the key at slot `highwater-1`).

The tombstone at slot 0 does not affect the ordering check. New inserts always compare against the tail slot.

**Note:** The tail comparison applies even when `live_count == 0` because tombstoned slots preserve their key bytes and participate in binary search. Allowing out-of-order inserts after deletions would break the sorted invariant.

### Capacity exhaustion

If a `Put` would require allocating a new slot and `slot_highwater >= slot_capacity`, `Commit` MUST return `ErrFull`.

---

## Validation

Open-time validation MUST be lightweight and MUST NOT scan all slots.

### Required checks

Each check specifies which error to return on failure.

**File size (before header read):**

| Check | Error |
|-------|-------|
| File length = 0 | (handled as create, see Open) |
| File length 1–255 | `ErrCorrupt` |
| File length ≥ 256 | Continue to header validation |

**Format identification (ErrIncompatible — wrong/unknown format):**

| Check | Error |
|-------|-------|
| `magic` ≠ "SLC1" | `ErrIncompatible` |
| `version` ≠ 1 | `ErrIncompatible` |
| `header_size` ≠ 256 | `ErrIncompatible` |
| `hash_alg` not supported | `ErrIncompatible` |
| `flags` has unknown bits | `ErrIncompatible` |
| Reserved bytes non-zero | `ErrIncompatible` |

**Configuration match (ErrIncompatible — file doesn't match caller's expectations):**

| Check | Error |
|-------|-------|
| `key_size` ≠ caller option | `ErrIncompatible` |
| `index_size` ≠ caller option | `ErrIncompatible` |
| `user_version` ≠ caller option | `ErrIncompatible` |
| `slot_capacity` ≠ caller option (if specified) | `ErrIncompatible` |
| Derived `slot_size` ≠ persisted `slot_size` | `ErrIncompatible` |

**Structural integrity (ErrCorrupt — file claims to be slotcache but is broken):**

| Check | Error |
|-------|-------|
| `header_crc32c` mismatch | `ErrCorrupt` |
| `slots_offset` ≠ 256 | `ErrCorrupt` |
| `buckets_offset` ≠ `slots_offset + slot_capacity * slot_size` | `ErrCorrupt` |
| File length < `buckets_offset + bucket_count * 16` | `ErrCorrupt` |
| `slot_highwater` > `slot_capacity` | `ErrCorrupt` |
| `live_count` > `slot_highwater` | `ErrCorrupt` |
| `bucket_count` not power of two or < 2 | `ErrCorrupt` |
| `bucket_used + bucket_tombstones` ≥ `bucket_count` | `ErrCorrupt` |
| `bucket_used` ≠ `live_count` | `ErrCorrupt` |

**Generation (ErrBusy or ErrCorrupt — see Open flow):**

| Check | Error |
|-------|-------|
| `generation` is odd | (handled per Open flow) |

Implementations MAY sample-check a small number of buckets for out-of-range slot IDs.

---

## Error model

All exported errors are **classification codes**. Implementations MAY wrap them with additional context. Callers MUST classify errors using `errors.Is(err, ErrX)` (or equivalent), not string matching.

| Error | When | Recovery |
|-------|------|----------|
| `ErrCorrupt` | Structural corruption detected | Delete and recreate |
| `ErrIncompatible` | Format/config mismatch | Delete and recreate |
| `ErrBusy` | Writer active or lock contention | Retry later |
| `ErrFull` | Slot capacity exhausted | Recreate with larger capacity |
| `ErrClosed` | Cache or Writer already closed | Fix caller code |
| `ErrWriteback` | msync failed (commit completed, durability not guaranteed) | Recreate if durability required |
| `ErrInvalidInput` | Bad arguments (wrong length, malformed spec) | Fix caller code |
| `ErrUnordered` | Ordered operation on non-ordered file | Use different API or recreate with ordered mode |
| `ErrOutOfOrderInsert` | Insert violates ordering constraint | Rebuild with sorted data |
