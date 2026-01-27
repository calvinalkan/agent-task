# slotcache v1 — Throwaway mmap cache (no WAL)

This document defines the **format and behavioral semantics** of *slotcache v1* in a single place.
It intentionally targets a **throwaway** cache: the authoritative source of truth is external
(typically the filesystem). If slotcache cannot prove its on-disk image is safe, it **rejects**
the file and callers rebuild from the authoritative source.

Normative keywords **MUST**, **SHOULD**, **MAY** are to be interpreted as in RFC 2119.

---

## Goals

slotcache v1 is designed to:

- Provide **fast read-mostly access** via an `mmap(MAP_SHARED)`-friendly layout.
- Support **multi-reader, single-writer** concurrency across threads and processes.
- Provide a **correct-or-retry** reader model (seqlock) while a writer is publishing.
- Provide a **clear rebuild signal** after power loss / crash during writes:
  - `Open()` either returns a fully usable cache, or returns `ErrNeedsRebuild`.
- Keep the on-disk hash index (buckets) **always consistent** with slots at every published snapshot.

## Non-goals

- No write-ahead log (WAL), no partial recovery, no “salvage” mode.
- No durability guarantees for individual `Commit()` calls (unless a `Checkpoint()` is performed).
- No protection against long-term bit-rot.
- No database-style multi-key transactions across crash/power loss.
- No guarantees under storage stacks that lie about durability barriers.

---

## Key concepts

### Authoritative source

The authoritative source (filesystem, database, etc.) is the truth. slotcache stores derived metadata.
When slotcache returns `ErrNeedsRebuild`, callers SHOULD delete/recreate the cache from the source.

### Snapshot generations (seqlock)

The header field `generation` is a **seqlock counter**:

- **even**: stable published snapshot
- **odd**: writer is publishing

Writers increment `generation` monotonically (MUST NOT “toggle the low bit”).

### Dirty vs clean (rebuild gating)

The header field `state` contains a lifecycle state. slotcache uses it to decide whether the file
is safe to open after unexpected termination.

- `STATE_CLEAN` (0): last checkpoint completed; file is safe to open (subject to validation).
- `STATE_DIRTY` (2): file has been modified since last checkpoint; after a crash/power loss it may
  be inconsistent and **MUST NOT** be opened unless an active writer is present.
- `STATE_INVALIDATED` (1): terminal; file MUST NOT be used and callers must recreate.

The difference between `STATE_DIRTY` and `generation` odd is important:
- `generation` odd is about **in-progress publish** (readers retry / ErrBusy).
- `STATE_DIRTY` is about **reboot safety** (reject/rebuild when no writer is active).

---

## Recommended I/O model (non-normative but strongly recommended)

- Readers map the file **read-only**: `mmap(PROT_READ, MAP_SHARED)`.
- Writers perform all slot/bucket modifications via **`pwrite()`/`pwritev()`** to known offsets
  (not via writable mmap).
- Durability barriers (for rebuild detection and checkpointing) use **`fdatasync(fd)`**
  (or `fsync(fd)`), and any failure poisons the cache.

Rationale: writable mmap stores do not provide a syscall return path per write; error reporting for
background writeback is harder to reason about. For caches that want to reliably detect flush errors,
explicit writes plus `fdatasync` are the simplest contract.

---

# On-disk format (v1)

All numeric fields are little-endian.

## File layout

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

## Header layout (256 bytes)

Offsets are bytes from file start.

```
0x000: magic[4]              = "SLC1"
0x004: version u32           = 1
0x008: header_size u32       = 256
0x00C: key_size u32
0x010: index_size u32
0x014: slot_size u32
0x018: hash_alg u32          (1 = FNV-1a 64-bit)
0x01C: flags u32

0x020: slot_capacity u64
0x028: slot_highwater u64
0x030: live_count u64
0x038: user_version u64
0x040: generation u64        (8-byte aligned; atomic 64-bit ops; seqlock)
0x048: bucket_count u64
0x050: bucket_used u64
0x058: bucket_tombstones u64
0x060: slots_offset u64      (== 256 in v1)
0x068: buckets_offset u64

0x070: header_crc32c u32     (CRC32-C, Castagnoli)
0x074: state u32             (slotcache-owned; see State field below)
0x078: user_flags u64        (caller-owned; opaque)
0x080: user_data[64]byte     (caller-owned; opaque)
0x0C0: reserved[64]byte      (MUST be 0 through 0x0FF)
```

### State field

`state` is a slotcache-owned field indicating lifecycle state:

- `STATE_CLEAN = 0` — cache is checkpointed and can be opened (subject to validation)
- `STATE_INVALIDATED = 1` — terminal; callers must recreate
- `STATE_DIRTY = 2` — modified since last checkpoint; if no writer is active, `Open()` MUST return `ErrNeedsRebuild`

If `state` contains an unknown value, implementations MUST return `ErrIncompatible`.

### Header CRC rules

`header_crc32c` MUST be computed as CRC32-C (Castagnoli) over the 256-byte header with:

- the `header_crc32c` field bytes set to zero, and
- the `generation` field bytes set to zero

This allows `generation` to change for seqlock publish without recomputing CRC for transient values.

The CRC covers all other header bytes including `state`, `user_flags`, and `user_data`.

### Header counter invariants

At any published (stable even `generation`) state where the file is considered usable (see `Open()`),
the following MUST hold:

- `0 <= slot_highwater <= slot_capacity`
- `0 <= live_count <= slot_highwater`
- `bucket_count` is a power of two and `bucket_count >= 2`
- `0 <= bucket_used <= bucket_count`
- `0 <= bucket_tombstones <= bucket_count`
- `bucket_used + bucket_tombstones < bucket_count` (at least one EMPTY bucket)
- **Primary index invariant:** `bucket_used == live_count`

If any invariant fails under a stable even `generation` in a file that is otherwise eligible to open,
the file MUST be treated as requiring rebuild.

### Header flags

`flags` is format-owned. Unknown bits MUST cause `ErrIncompatible`.

Defined flags:

- `FLAG_ORDERED_KEYS = 1 << 0`
  - Slots are globally sorted by key bytes in slot-id order.
  - Writer MUST enforce append-only ordered inserts.
  - Deleted slot key bytes MUST be preserved (do not overwrite key bytes).

---

## Slots layout

Slots are fixed-size and 8-byte aligned.

Let:

- `key_pad = (8 - (key_size % 8)) % 8`

Then:

```
slot_size = align8( meta(8) + key_size + key_pad + revision(8) + index_size )
```
where `align8(x) = (x + 7) &^ 7`.

Each slot contains:

| Field | Type | Meaning |
|---|---:|---|
| meta | u64 | internal flags |
| key | [key_size]byte | key bytes |
| key_pad | bytes | padding |
| revision | i64 | caller-owned opaque |
| index | [index_size]byte | caller-owned opaque |
| padding | bytes | zero padding to slot_size |

`meta` bits (v1):

- bit 0: `USED` (1=live, 0=deleted)

All other bits are reserved and MUST be zero.

Slot IDs are append-only; deletes create tombstoned slots and slot IDs are never reused.

---

## Buckets layout (hash index)

Open-addressed hash table with linear probing.

Each bucket is 16 bytes:

| Field | Type | Meaning |
|---|---:|---|
| hash64 | u64 | hash of key bytes |
| slot_plus1 | u64 | 0=EMPTY, 0xFFFFFFFFFFFFFFFF=TOMBSTONE, else slot_id+1 |

Bucket states:

- EMPTY: `slot_plus1 == 0`
- TOMBSTONE: `slot_plus1 == 0xFFFFFFFFFFFFFFFF`
- FULL: otherwise; `slot_id = slot_plus1 - 1`

v1 uses FNV-1a 64-bit over key bytes.

Lookups MUST verify key bytes match (hash is a hint).

---

# Behavioral semantics

## Concurrency model

- **Multi-reader, single-writer**
- Readers are lock-free (no interprocess locks).
- Writers MUST be mutually exclusive (at most one active writer at a time).

### Writer lock (recommended)

Implementations SHOULD use an advisory lock file `{cache_path}.lock`.

- Writers acquire an **exclusive** lock for the duration of a write session.
- If locking is disabled, callers MUST provide equivalent external synchronization.

### Seqlock publish requirements

- `generation` MUST be 8-byte aligned and updated using atomic 64-bit operations.
- Writer MUST use release semantics when publishing the final even generation.
- Reader MUST use acquire semantics when loading generation for snapshot validation.

Platforms that cannot provide aligned atomic 64-bit operations across processes are out of scope.

---

## Reader coherence rule (seqlock)

All read operations MUST be correct-or-retry:

1. Read `g1 = generation` atomically.
2. If `g1` is odd: retry; after bounded retries return `ErrBusy`.
3. Perform the read (bucket lookup / scan).
4. Read `g2 = generation` atomically; if `g1 != g2`, retry; otherwise return results.

If an impossible invariant is observed during the read, the reader MUST re-check generation:
- if generation changed or is odd: treat as overlap and retry
- otherwise: treat as rebuild-required

Reads MUST not retry forever; implementations MUST eventually return `ErrBusy`.

---

## Open

`Open(path, opts)` opens or creates a cache.

### Open eligibility

`Open()` MUST return a usable Cache handle only if:

- Header validates (magic/version/header_size, CRC, reserved bytes zero, config match).
- `generation` is stable even (or can be read as stable even via bounded retry).
- `state` is not `STATE_INVALIDATED`.
- If `state == STATE_DIRTY`, the implementation MUST NOT open the file as usable unless it can prove an active writer exists (see below).

### DIRTY handling

If `state == STATE_DIRTY`:

- If locking is enabled:
  - If the writer lock is **held** by another process, `Open()` MAY succeed as a read-only Cache handle.
    Readers will observe the last published snapshot via seqlock. The cache is not considered checkpointed.
  - If the writer lock is **not held**, `Open()` MUST return `ErrNeedsRebuild`.
- If locking is disabled:
  - `Open()` MUST return `ErrNeedsRebuild` unless the caller provides an out-of-band guarantee that a writer is currently active and the file is not being opened after a crash.

Rationale: `STATE_DIRTY` with no active writer indicates a previous run modified the file but did not complete a checkpoint,
which is indistinguishable from a crash/power loss during writes.

### Odd generation handling

If a stable even generation cannot be obtained because `generation` remains odd:

- If locking is enabled and the writer lock is held: `Open()` SHOULD return `ErrBusy`.
- Otherwise: `Open()` MUST return `ErrNeedsRebuild`.

### File creation

Creation SHOULD use temp + rename in the same directory to avoid partial files at the target path.

New files MUST be initialized as:

- `state = STATE_CLEAN`
- `generation = 0`
- counters set to zero
- header CRC computed

---

## Writer sessions

Writes occur within a writer session.

### BeginWrite

`BeginWrite()` acquires exclusive write access.

- With locking enabled: acquire the writer lock (non-blocking); on contention return `ErrBusy`.
- With locking disabled: assume equivalent external exclusivity.

If the file is in `STATE_CLEAN`, the writer MUST transition it to `STATE_DIRTY` **before performing any slot/bucket modifications**:

1. Write header with `state = STATE_DIRTY` and updated `header_crc32c` (generation excluded).
2. Call `fdatasync(fd)` (or equivalent) to make the DIRTY marker durable.
3. If this sync fails, the writer MUST treat the cache as poisoned and return `ErrNeedsRebuild`.
   Implementations SHOULD set `state = STATE_INVALIDATED` (best-effort) to force existing readers to reopen.

After `BeginWrite`, the file remains DIRTY until a successful `Checkpoint()` completes.

### Put/Delete buffering

Implementations MAY buffer operations (last-op-wins) to minimize publish windows.
No database-style rollback is implied; buffering is only an optimization and a way to enforce ordered-keys constraints.

### Commit (publish)

`Commit()` publishes buffered operations to readers. It does **not** guarantee durability.

Writer MUST:

1. Advance `generation` to a new odd value (publish “writer in progress”).
2. Apply all staged operations, updating:
   - slots (including tombstone semantics)
   - buckets (open addressing rules)
   - header counters (`slot_highwater`, `live_count`, `bucket_used`, `bucket_tombstones`)
3. Recompute `header_crc32c` (with generation bytes treated as zero).
4. Advance `generation` to a new even value.

After `Commit()`, readers observe either the previous snapshot or the new one; partial states are retried away via seqlock.

### Checkpoint (durability point)

`Checkpoint()` makes the cache reopenable without rebuild by flushing dirty data and marking the file CLEAN.

Writer MUST:

1. Call `fdatasync(fd)` (or equivalent). If it fails:
   - return `ErrNeedsRebuild`
   - and the cache SHOULD be treated as poisoned for further use
2. Write header with `state = STATE_CLEAN` and updated `header_crc32c`.
   A second durability barrier for this header write is OPTIONAL: if omitted, a power loss may cause the file
   to appear DIRTY on the next boot even though the data flush succeeded, which is acceptable for a throwaway cache.

After a successful checkpoint, a subsequent `Open()` on the same boot SHOULD see the CLEAN state via the OS cache.
Across a reboot/power loss, callers MUST be prepared for the cache to require rebuild.

### Close

Closing a writer without checkpointing leaves the file in `STATE_DIRTY`.
If no writer is active, the next `Open()` MUST return `ErrNeedsRebuild`.

---

## Invalidate (terminal)

`Invalidate()` marks a cache file unusable (solves “mmap maps inode, not path” stale-handle issues).

Behavior:

- Acquire writer exclusivity (writer lock + in-process guard).
- Publish invalidation via seqlock:
  1) set `generation` odd
  2) set `state = STATE_INVALIDATED`, recompute header CRC
  3) set `generation` even
- After invalidation, all operations on any handle that observes the invalidated state MUST return `ErrInvalidated`.

Invalidation is terminal; callers must recreate the cache file.

---

# Validation

`Open()` MUST be lightweight and MUST NOT scan all slots.

Required checks include:

- file size (0, 1–255, ≥256 behavior)
- magic/version/header_size
- reserved bytes are zero
- header CRC matches
- offsets/lengths are consistent with file size
- counter invariants (listed above)

If any required check fails, `Open()` MUST return either `ErrIncompatible` (format/config mismatch) or `ErrNeedsRebuild`
(structural breakage / dirty state / crashed writer).

Implementations MAY sample-check a small number of buckets for out-of-range slot IDs.

---

# Error model

Errors are classification codes; callers rebuild on `ErrNeedsRebuild`.

- `ErrNeedsRebuild`: file is DIRTY with no active writer, crashed writer detected, structural corruption, or any durability-barrier (`fdatasync`) failure.
- `ErrIncompatible`: magic/version/config mismatch; recreate with correct parameters.
- `ErrInvalidated`: cache invalidated; recreate.
- `ErrBusy`: writer active / generation unstable; retry later.
- `ErrFull`: slot capacity exhausted; recreate larger.
- `ErrOutOfOrderInsert`: ordered-keys constraint violated; rebuild from sorted source.
- `ErrInvalidInput`: wrong key/index lengths or malformed prefix spec.
- `ErrClosed`: handle already closed.

---

## Guarantees (summary)

- If `Open()` returns a usable Cache, the cache satisfies header invariants and bucket/slot consistency at a stable generation.
- While a writer publishes, readers are correct-or-retry via seqlock.
- If the file is left DIRTY without an active writer (crash/power loss during or after writes), `Open()` returns `ErrNeedsRebuild`.
- `Commit()` publishes to readers but does not promise durability.
- `Checkpoint()` is the only durability point; `fdatasync` failure poisons the cache and requires rebuild.
