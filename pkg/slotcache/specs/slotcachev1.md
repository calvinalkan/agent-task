# slotcache v1: mmap-friendly slot cache format ("SLC1")

slotcache is a **throwaway, file-backed cache** optimized for:

- **Fast reads** via `mmap` (Unix).
- **Fast O(n) filtering** by scanning a dense slot array sequentially.
- **Fast point lookups and updates** via a persisted `key → slot_id` hash index.
- **Cheap invalidation/reset** (cache semantics; not a primary data store).

This document specifies:

- Public API contract (behavior + error model)
- On-disk format v1 (magic `"SLC1"`)
- Concurrency and crash behavior (single-writer + multi-reader)

---

## Normative language

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are used as follows:

- **MUST / MUST NOT**: absolute requirements for a conforming implementation.
- **SHOULD / SHOULD NOT**: strong recommendations; valid reasons may exist to deviate.
- **MAY**: optional behavior.

In this document:

- **implementation** refers to the slotcache library.
- **caller** refers to application code using the library.

---

## Goals

- **Scan-fast filtering:** scanning slots sequentially should be close to “memory bandwidth limited”.
- **Fast point ops:** expected O(1) (amortized under a good hash distribution and reasonable load factor) `Get`, `Put`, `Delete`; worst-case O(n) under heavy clustering/collisions.
- **Opaque index bytes:** slotcache does not understand schemas; it stores fixed-size bytes per slot.
- **Sound reads (no false positives):** read APIs MUST NOT return an entry unless the returned key bytes match the requested key/prefix under a stable even generation.
- **Fail-fast on detected corruption:** if a read detects a structural invariant violation under a stable even generation, it MUST return `ErrCorrupt` (with details).
- **Simple invalidation:** detect config/schema mismatch via persisted header fields; return `ErrIncompatible`.

## Non-goals (v1)

- Durable database semantics (cache is throwaway).
- Self-healing or automatic rebuild from source-of-truth data (the library does not know your data universe).
- Multi-writer concurrency / merging across processes.
- Variable-length “data blob” section (v1 is index-only; callers fetch source-of-truth data).

---

## Local filesystem assumptions

slotcache targets **local filesystems** (e.g., APFS on macOS, ext4/xfs on Linux) with POSIX-like semantics:

- `mmap` provides a coherent view of a regular file on a single machine.
- `rename(old, new)` within the same directory is atomic.
- Advisory file locks (e.g., `flock`) behave consistently for coordinating a single writer.

slotcache is **not designed or tested** for network/distributed filesystems or sync layers
(e.g., NFS/SMB, FUSE-based mounts, cloud-sync folders). On such systems, one or more of the above
assumptions may not hold (locking semantics, rename atomicity, mmap consistency, delayed visibility),
and behavior is undefined.

### External truncation is out of scope

If another process truncates or overwrites the cache file while it is mapped, the OS may raise SIGBUS
or otherwise terminate the process. slotcache does not attempt to defend against this; callers MUST
treat the cache file as library-owned and MUST NOT truncate it while in use.

---

## Concepts

### Key

- A **key** is a fixed-size byte string of length `KeySize`.
- slotcache v1 treats keys as **opaque bytes** (no parsing / no validation beyond length).

### Key prefix

Some read APIs may operate on a **prefix** of a key.

- A **prefix** is a byte string of length `1..KeySize`.
- A key `k` matches a prefix `p` iff `k[0:len(p)] == p`.
- Prefix APIs MUST return `ErrInvalidPrefix` if `len(p) == 0` or `len(p) > KeySize`.

### Slot

A **slot** is a fixed-size record stored in the slots section.

Each slot contains:

- `meta` (internal flags; includes used/tombstone state)
- `key` (fixed-size bytes)
- `revision` (opaque `int64` provided by caller; typically source-of-truth mtime/generation)
- `index` (fixed-size opaque bytes for filtering)

Slot IDs are **append-only** in v1:

- new keys are assigned monotonically increasing slot IDs from `0..(slot_highwater-1)`
- slot IDs are never reused; deleting a key marks its slot as a tombstone
- `Put` for an existing key MAY update the existing slot in place (no new slot ID)

### Index bytes

`index` is a fixed-size byte array per slot (`IndexSize` bytes).

- slotcache does not interpret or validate these bytes.
- Higher layers compile schemas (enums, numbers, padded strings, bitsets) into this byte layout.

### Revision

`revision` is an `int64` stored verbatim. It is **not interpreted** by slotcache.

Typical usage: store source file mtime (ns) or a logical generation number to detect staleness
without re-reading every file (e.g., verify only the returned page).

---

## Public API contract (conceptual)

The API is intentionally low-level and bytes-in/bytes-out.

### Options

```go
type LockMode int

const (
    // LockFlock uses an advisory OS lock (e.g., flock) to coordinate a single writer process.
    // This is the default.
    LockFlock LockMode = iota

    // LockNone disables interprocess locking; callers MUST enforce single-writer externally.
    LockNone
)

type WritebackMode int

const (
    // WritebackNone does not call msync.
    WritebackNone WritebackMode = iota // default

    // WritebackAsync calls msync(MS_ASYNC) at an implementation-defined cadence (SHOULD).
    WritebackAsync

    // WritebackSync calls msync(MS_SYNC) as specified by WritebackOrder.
    WritebackSync
)

type WritebackOrder int

const (
    // WritebackAfterPublish publishes first, then attempts writeback.
    WritebackAfterPublish WritebackOrder = iota // default

    // WritebackBeforePublish attempts writeback while generation is odd, then publishes.
    // Valid only when WritebackMode==WritebackSync.
    WritebackBeforePublish
)

type Options struct {
    Path string

    KeySize     int    // persisted; default 16
    IndexSize   int    // persisted; default 0
    UserVersion uint64 // persisted; default 1

    SlotCapacity uint64 // required when creating; if non-zero when opening, MUST match persisted slot_capacity
    HashLoadFactor     float64 // default 0.75; MUST satisfy 0 < LF < 1; used to size bucket_count at creation
    MaxTombstoneFactor float64 // default 0.20; MUST satisfy 0 <= TF < 1; writer MAY rehash to reduce bucket_tombstones when exceeded

    // Locking
    LockMode LockMode // default LockFlock
    LockPath string   // default Path + ".lock"

    // Writeback
    WritebackMode  WritebackMode  // default WritebackNone
    WritebackOrder WritebackOrder // default WritebackAfterPublish
}
```


#### Option normalization and validation

Implementations MUST normalize and validate `Options` before use.

Normalization (defaults):

- If `KeySize == 0`, treat as `16`.
- If `UserVersion == 0`, treat as `1`.
- If `HashLoadFactor == 0`, treat as `0.75`.
- If `MaxTombstoneFactor == 0`, treat as `0.20`.
- If `LockPath == ""`, treat as `Path + ".lock"`.

Validation (reject with `ErrInvalidInput`):

- `Path` MUST be non-empty.
- `KeySize` MUST be `> 0` and MUST fit in a persisted `u32`.
- `IndexSize` MUST be `>= 0` and MUST fit in a persisted `u32`.
- `HashLoadFactor` MUST satisfy `0 < HashLoadFactor && HashLoadFactor < 1`.
- `MaxTombstoneFactor` MUST satisfy `0 <= MaxTombstoneFactor && MaxTombstoneFactor < 1`.
- When **creating** a new file, `SlotCapacity` MUST be `> 0`.
- `SlotCapacity` MUST be `<= 0xFFFFFFFFFFFFFFFE` (bucket sentinel constraint; see Buckets layout).
- The derived `slot_size` and total file size computations MUST be performed with overflow checks; if they overflow or exceed implementation limits (e.g., cannot `mmap`), return `ErrInvalidInput`.

### Error classification + details

All exported errors (`ErrCorrupt`, `ErrBusy`, etc.) are **classification codes**.

Implementations MAY return errors that wrap these codes with additional context (message, reason, underlying OS error).
Callers MUST classify errors using `errors.Is(err, ErrX)` (or equivalent), not by string matching.

Typed errors MAY be returned to expose structured details while still classifying to a code via `Unwrap()`.

Example: `WritebackError` that unwraps to `ErrWriteback` and contains `Published bool`.

---

## Concurrency model

- **Multi-reader, single-writer.**
- Readers are lock-free (they do not take `flock`).
- Writers hold an exclusive writer lock for the entire write session (W1).

### Seqlock generation counter

The header field `generation` is a seqlock counter:

- even = stable snapshot
- odd  = writer in progress

`generation` MUST be a monotonically increasing 64-bit counter. Writers MUST NOT publish by only toggling the low bit.

Implementations MUST update `generation` with **atomic 64-bit** loads/stores and **acquire/release (or stronger) memory ordering** such that:

- a reader that observes an even `generation` also observes all slot/bucket/header writes that happened-before that publication,
- a writer does not allow slot/bucket/header writes to be reordered after publishing the final even `generation`, and
- a writer does not allow the commit's slot/bucket/header writes to become visible before publishing the initial odd `generation`.

Platforms that cannot perform aligned atomic 64-bit operations across processes are out of scope for v1.


#### Go implementation note (v1)

For the Go implementation, `generation` MUST be accessed using `sync/atomic` (for example, `atomic.Uint64.Load/Store/Add` or `atomic.LoadUint64/StoreUint64/AddUint64`).

These operations are sequentially consistent in Go and act as the required ordering fences between the `generation` publication and ordinary loads/stores of mmapped slot/bucket bytes.

Recommended reader pattern:

```go
for tries := 0; tries < maxRetries; tries++ {
    g1 := gen.Load() // atomic
    if g1&1 == 1 {
        backoff(tries)
        continue
    }

    // Read slots/buckets non-atomically here.

    g2 := gen.Load() // atomic
    if g1 == g2 {
        return ok
    }
}
return ErrBusy
```

Recommended writer commit pattern (conceptual):

```go
// Preconditions: writer holds W1 lock; generation is even.
_ = gen.Add(1) // publish odd
// ... apply slot/bucket/header writes ...
_ = gen.Add(1) // publish even
```

(Exact increment mechanics may differ, but the two atomic publications and their ordering MUST hold.)

Readers MUST treat any change in `generation` as overlap and retry (bounded).

---

## Open / initialization requirements

### Open must not leave garbage behind

If `Open` fails while creating/initializing a new cache file, it MUST NOT leave a partially initialized file at `opts.Path`.
(Implementations SHOULD create a temporary file and `rename` it into place only after it is fully initialized.)

### Why generation alone is not sufficient for init safety (note)

`generation` is an *in-file* coherence mechanism once a valid header exists. It does not, by itself, prevent other processes
from observing a zero-length file, a partially written header, or a partially truncated layout during initialization.
Therefore initialization SHOULD use one of: temp+rename, O_EXCL create + cleanup on error, and/or a writer lock.

### Open with an existing file

`Open` MUST validate:

- header magic/version
- header bounds and section sizes fit the file
- header CRC rules
- option compatibility (`KeySize`, `IndexSize`, `UserVersion`, and (if non-zero) `SlotCapacity`)
- basic invariants (see Validation rules)

### Open with odd generation

If `generation` is odd at `Open`:

- If `LockMode==LockNone`, `Open` MUST return `ErrBusy`.
- If `LockMode==LockFlock`, `Open` MUST attempt to acquire the writer lock in non-blocking mode:
  - If the lock cannot be acquired: return `ErrBusy` (writer active).
  - If the lock is acquired: re-read `generation`.
    - If it is now even: proceed.
    - If it remains odd: return `ErrCorrupt` with details (incomplete commit or corruption).

---

## Reading (seqlock + fail-fast anomalies)

All read operations MUST be correct-or-retry under the seqlock:

1. Read `g1 = generation`. If odd, retry with bounded backoff (implementation-defined); if it remains odd after bounded retries, return `ErrBusy`.
2. Perform the read (scan / hash lookup).
3. If an impossible invariant is observed during the read:
   - immediately re-read `gX = generation`
   - if `gX` is odd OR `gX != g1`: treat as overlap and retry/ErrBusy
   - else (stable even generation): return `ErrCorrupt` with details (R-FAIL)
4. Read `g2 = generation`. If `g1 != g2`: retry (bounded). Else return results.

Reads MUST be bounded (no infinite loops) even under corruption.

---

## Writing (single-writer sessions)

### Writer lock (W1)

If `LockMode==LockFlock`:

- `BeginWrite()` MUST acquire the writer lock and hold it until `Writer.Commit/Abort/Close` completes.
- `Open()` MAY also acquire the writer lock for initialization or to classify odd-generation cases as Busy vs Corrupt.

If `LockMode==LockNone`:

- caller MUST enforce a single-writer policy externally.

### Buffered ops and “effective state”

- `Put` and `Delete` buffer operations in memory and MUST NOT publish changes until `Commit`.
- For a given key within one writer session, the **last buffered operation wins** at commit.
- `Delete(key) (bool, error)` returns whether the key was **effectively present immediately before the call**,
  considering the on-disk state at `BeginWrite` plus buffered ops so far.

### Publish protocol

At `Commit`, the writer MUST:

1. Increment `generation` to an odd value (writer in progress) using an atomic store (seq-cst or equivalent barrier), and perform no commit writes before this store.
2. Apply buffered ops to slots, buckets, and header counters.
3. Recompute and store header CRC (excluding generation; see CRC rules).
4. Publish by incrementing `generation` to a new even value. This final publication MUST occur only after all writes in steps (2)-(3) are complete, and MUST use an atomic store (release or stronger).
5. Optionally perform writeback per writeback settings.

If `generation` would overflow/wrap, the implementation MUST treat the file as unusable and return `ErrCorrupt`/`ErrIncompatible` (caller should delete/recreate).

---

## Writeback semantics (msync)

Writeback controls best-effort flushing of dirty mmapped pages to the backing file (as defined by `msync`).

Writeback is **not** required for interprocess visibility (MAP_SHARED already provides that); it primarily affects durability and dirty memory pressure.

Note: `msync(MS_SYNC)` is not a universal power-failure durability guarantee on all filesystems/storage stacks; stronger guarantees typically require `fsync`/`fdatasync`, which is out of scope for v1.

### Valid combinations (validated at Open)

Valid:

- (None, AfterPublish)
- (Async, AfterPublish)
- (Sync, AfterPublish)
- (Sync, BeforePublish)

Invalid:

- (None, BeforePublish)
- (Async, BeforePublish)

Invalid combos MUST return `ErrInvalidInput` with details.

### WritebackAsync

Implementations SHOULD call `msync(MS_ASYNC)` at an implementation-defined cadence (e.g., every commit or coalesced).

### WritebackSync ordering

- **AfterPublish:** publish first, then call `msync(MS_SYNC)` (range or whole mapping).
  - If msync fails, `Commit()` MUST return an error wrapping `ErrWriteback`.
  - The returned typed `WritebackError` MUST have `Published=true`.
  - Commit may have already been published.

- **BeforePublish:** call `msync(MS_SYNC)` while generation is odd, then publish.
  - If msync fails, `Commit()` MUST return an error wrapping `ErrWriteback` with `Published=false`,
    and MUST NOT publish (generation remains odd). In this state the file represents an incomplete commit and SHOULD be treated as corrupt/throwaway (delete/recreate).

---

## On-disk format (v1)

All numeric fields are little-endian.

### File layout

```
Offset 0
┌───────────────────────────────┐
│ Header (fixed, 256 bytes)     │
└───────────────────────────────┘
┌───────────────────────────────┐
│ Slots section                 │  slot_capacity × slot_size bytes
└───────────────────────────────┘
┌───────────────────────────────┐
│ Buckets section (hash index)  │  bucket_count × 16 bytes
└───────────────────────────────┘
EOF
```

### Header layout (256 bytes, explicit offsets)

Offsets are bytes from file start. All fields are little-endian.

```
0x000: magic[4]              = "SLC1"
0x004: version u32           = 1
0x008: header_size u32       = 256
0x00C: key_size u32
0x010: index_size u32
0x014: slot_size u32
0x018: hash_alg u32          (1 = FNV-1a 64-bit)
0x01C: flags u32             (reserved; MUST be 0)

0x020: slot_capacity u64
0x028: slot_highwater u64
0x030: live_count u64
0x038: user_version u64
0x040: generation u64        (8-byte aligned; updated with atomic 64-bit ops; see seqlock rules)
0x048: bucket_count u64
0x050: bucket_used u64
0x058: bucket_tombstones u64
0x060: slots_offset u64      (== 256 in v1)
0x068: buckets_offset u64

0x070: header_crc32c u32     (CRC32-C, Castagnoli)
0x074: reserved_u32 u32      (MUST be 0)
0x078: reserved bytes...     (MUST be 0 through 0x0FF)
```

#### Header CRC rules

`header_crc32c` MUST be computed as CRC32-C (Castagnoli) over the 256-byte header with:

- the `header_crc32c` field bytes set to zero, and
- the `generation` field bytes set to zero

For the Go implementation, CRC32-C MUST be computed exactly as:

```go
crc32.Checksum(headerBytes, crc32.MakeTable(crc32.Castagnoli))
```

after zeroing the two fields above (and encoding/storing the resulting `uint32` little-endian into `header_crc32c`).

This allows `generation` to change for seqlock publish without recomputing CRC for transient values.

If reserved bytes are non-zero, implementations MUST return `ErrIncompatible`.

### Slot layout (revision aligned)

Slots are fixed-size and 8-byte aligned.

Let:

- `key_pad = (8 - (key_size % 8)) % 8`

Then:

```
slot_size = align8( meta(8) + key_size + key_pad + revision(8) + index_size )
```

`align8(x)` is defined as rounding `x` up to the next multiple of 8:

```
align8(x) = (x + 7) &^ 7
```

Each slot:

| Field | Type | Meaning |
|------|------|---------|
| meta | uint64 | internal flags |
| key | [key_size]byte | key bytes (meaningful only if live) |
| key_pad | bytes | zero padding so `revision` is 8-byte aligned |
| revision | int64 | opaque caller value |
| index | [index_size]byte | opaque bytes |
| padding | bytes | zero padding to slot_size |

`meta` bit layout (v1):

- bit 0: `USED` (1=live, 0=deleted)

All other bits are reserved and MUST be zero in v1.

### Buckets layout (hash index)

Open-addressed hash table with linear probing.

Each bucket is 16 bytes:

| Field | Type | Meaning |
|------|------|---------|
| hash64 | uint64 | hash of key bytes |
| slot_plus1 | uint64 | 0=EMPTY, ^uint64(0)=TOMBSTONE, else slot_id+1 |

Bucket states:

- EMPTY: `slot_plus1 == 0`
- TOMBSTONE: `slot_plus1 == 0xFFFFFFFFFFFFFFFF`
- FULL: otherwise; slot_id = slot_plus1 - 1

Constraints:

- `SlotCapacity` MUST be <= `0xFFFFFFFFFFFFFFFE` (because `slot_plus1` uses 0 and all-ones sentinels).

### Bucket sizing (creation)

When creating a new file, implementations MUST choose `bucket_count` such that:

- `bucket_count` is a power of two and `bucket_count >= 2`.
- `bucket_count = nextPow2( ceil(slot_capacity / HashLoadFactor) )`.

This ensures that even at maximum occupancy (`bucket_used == slot_capacity`), there is always at least one EMPTY bucket (required for bounded probes).

Definitions:

- `ceil(a/b)` is the smallest integer `>= a/b` for positive `a, b`.
- `nextPow2(x)` is the smallest power of two `>= x`.

### Hash algorithm

v1 uses **FNV-1a 64-bit** over the `key` bytes.

Constants:

- offset basis: `14695981039346656037` (`0xcbf29ce484222325`)
- prime: `1099511628211` (`0x100000001b3`)

Definition:

```
h = offset_basis
for each byte b in key:
    h = h XOR uint64(b)
    h = h * prime
return h
```

Note: FNV-1a is not cryptographically secure. Under attacker-controlled/adversarial keys, collision clustering can degrade performance; v1 assumes non-adversarial cache keys.

### Probe sequence (linear probing)

Given `h = hash64(key)` and `mask = bucket_count - 1`:

- start index: `i = h & mask`
- probe: `i = (i + 1) & mask` until found or an EMPTY bucket is encountered

On a FULL bucket, `hash64` is only a hint. Implementations MUST verify the candidate slot's key bytes against the requested key before returning a match. If `hash64` matches but key bytes do not, the lookup MUST continue probing.

Lookups MUST be bounded: if a probe visits `bucket_count` buckets without encountering EMPTY, the table violates the “at least one EMPTY bucket” invariant and is corrupt.


### Writer hash-table invariants and rehashing

At every **published** (stable even `generation`) state, the following MUST hold:

- `bucket_used == live_count`.
- `bucket_used + bucket_tombstones < bucket_count` (at least one EMPTY bucket).

Writers MUST ensure these invariants before publishing an even `generation`.

Tombstones:

- Deleting a key MUST set its bucket’s `slot_plus1` to TOMBSTONE and update `bucket_used/bucket_tombstones` accordingly.
- The `hash64` field in a TOMBSTONE bucket is ignored (it MAY be left unchanged).

Insertion (linear probing with tombstones):

- When probing for a key, writers MUST continue probing past TOMBSTONE buckets (they do not terminate the search).
- Writers MAY remember the first TOMBSTONE encountered and reuse it for insertion **only if** the key is not found later in the probe sequence.

Rehashing:

- If `bucket_tombstones / bucket_count > MaxTombstoneFactor`, writers SHOULD rehash (rebuild the buckets from the live slots), resetting `bucket_tombstones` to `0`.
- If an insertion would require probing `bucket_count` buckets without finding an EMPTY bucket, writers MUST rehash/resize or return `ErrCorrupt`/`ErrFull`; they MUST NOT spin indefinitely.

---

## Open-time validation rules

Open-time validation MUST be lightweight and MUST NOT scan all slots.

Required checks:

- file length >= 256
- magic/version/header_size match
- flags == 0 and hash_alg is supported (== 1 in v1)
- `generation` is even or is handled per the odd-generation Open rules above
- header CRC32-C matches (CRC rules above)
- reserved bytes are zero (else ErrIncompatible)
- persisted key_size/index_size/user_version match options (else ErrIncompatible)
- if opts.SlotCapacity != 0, it matches persisted slot_capacity (else ErrIncompatible)
- derived slot_size matches persisted slot_size
- slot_capacity > 0
- slots_offset == 256
- buckets_offset == slots_offset + slot_capacity*slot_size (computed with checked arithmetic; no u64 overflow)
- file length >= buckets_offset + bucket_count*16 (computed with checked arithmetic; no u64 overflow)
- slot_highwater <= slot_capacity
- live_count <= slot_highwater
- bucket_count is power of two and >= 2
- bucket_used + bucket_tombstones < bucket_count  (at least one EMPTY bucket)

Implementations MUST additionally check `bucket_used == live_count` (counter consistency), but MUST NOT treat that as sufficient to prove index correctness.

---

## Suggested error codes (non-exhaustive)

Rebuild-class:

- `ErrCorrupt` (structural corruption or detected invariant violation)
- `ErrIncompatible` (magic/version mismatch, or KeySize/IndexSize/UserVersion mismatch)

Operational:

- `ErrBusy`
- `ErrInvalidInput`
- `ErrInvalidKey`
- `ErrInvalidPrefix`
- `ErrWriteback` (msync failure; typed error includes Published flag)
- `ErrFull`
- `ErrClosed`
