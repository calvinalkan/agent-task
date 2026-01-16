# On-disk format

All numeric fields are little-endian.

---

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

---

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
0x074: reserved_u32 u32      (MUST be 0)
0x078: reserved bytes...     (MUST be 0 through 0x0FF)
```

### Value constraints

- `key_size` MUST be ≥ 1 (zero-length keys are invalid)
- `index_size` MAY be 0 (key + revision only, no index data)
- `slot_capacity` MUST be ≥ 1
- `slot_capacity` MUST be ≤ `0xFFFFFFFFFFFFFFFE` (bucket encoding constraint)

Header field sizes (u32 for sizes, u64 for counts) define maximum representable values. Implementations SHOULD reject configurations that would exceed platform limits (virtual address space, maximum file size).

### user_version

`user_version` is a caller-defined `u64` for schema versioning. slotcache stores it verbatim and does not interpret it.

On `Open`, if the caller's `user_version` option doesn't match the persisted value, the implementation MUST return `ErrIncompatible`.

**Typical use:** Increment `user_version` when your index byte encoding changes. This forces a cache rebuild rather than silently misinterpreting old data.

### Header flags

`flags` is a bitset owned by the slotcache format.

- If any unknown flag bit is set, implementations MUST return `ErrIncompatible`.

Defined flags:

- `FLAG_ORDERED_KEYS = 1 << 0`
  - The slots array is globally sorted by key bytes in slot-id order
  - Writer MUST enforce append-only ordered inserts
  - Tombstone key bytes MUST be preserved (delete MUST NOT overwrite key bytes)
  - Ordered cursor APIs are enabled

### Header CRC rules

`header_crc32c` MUST be computed as CRC32-C (Castagnoli) over the 256-byte header with:

- the `header_crc32c` field bytes set to zero, and
- the `generation` field bytes set to zero

This allows `generation` to change for seqlock publish without recomputing CRC for transient values.

If reserved bytes are non-zero, implementations MUST return `ErrIncompatible`.

### Header counter invariants

At any published (stable even `generation`) state, the following MUST hold:

- `0 <= slot_highwater <= slot_capacity`
- `0 <= live_count <= slot_highwater`
- `bucket_count` is a power of two and `bucket_count >= 2`
- `0 <= bucket_used <= bucket_count`
- `0 <= bucket_tombstones <= bucket_count`
- `bucket_used + bucket_tombstones < bucket_count` (at least one EMPTY bucket)
- **Primary index invariant:** `bucket_used == live_count`
  - Each live slot MUST have exactly one FULL bucket entry
  - There MUST NOT be bucket entries pointing to deleted slots

If any invariant fails under a stable even generation, the file is corrupt.

### Initial state (new file)

When creating a new cache file, the following initial values MUST be set:

- `slot_highwater = 0`
- `live_count = 0`
- `generation = 0` (even = stable, ready for readers)
- `bucket_used = 0`
- `bucket_tombstones = 0`

All slots and buckets are implicitly zero (sparse file). Zero-valued buckets are EMPTY (`slot_plus1 == 0`).

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

**Why 8-byte alignment?**

- **Atomicity**: The `meta` (u64) and `revision` (i64) fields must be read/written atomically for the seqlock to work correctly. On most architectures, atomic 64-bit operations require 8-byte alignment. Without it, reads could tear (observe half old value, half new value).
- **Performance**: Misaligned 64-bit access is slower on most CPUs (may cross cache line boundaries, require multiple memory operations).

The `key_pad` ensures `revision` is 8-byte aligned within each slot. The final `align8()` ensures each slot starts at an 8-byte boundary (so the next slot's `meta` is also aligned).

**Example**: `key_size=10, index_size=5`

| Field | Offset | Size | Notes |
|-------|--------|------|-------|
| meta | 0 | 8 | 8-byte aligned ✓ |
| key | 8 | 10 | |
| key_pad | 18 | 6 | `(8 - 10%8) % 8 = 6` |
| revision | 24 | 8 | 8-byte aligned ✓ |
| index | 32 | 5 | |
| (trailing pad) | 37 | 3 | to reach slot_size=40 |

`slot_size = align8(8 + 10 + 6 + 8 + 5) = align8(37) = 40`

Each slot:

| Field | Type | Meaning |
|-------|------|---------|
| meta | u64 | internal flags |
| key | [key_size]byte | key bytes |
| key_pad | bytes | padding so revision is 8-byte aligned |
| revision | i64 | opaque caller value |
| index | [index_size]byte | opaque index bytes |
| padding | bytes | zero padding to slot_size |

`meta` bit layout (v1):

- bit 0: `USED` (1=live, 0=deleted)

All other bits are reserved and MUST be zero in v1.

**Deleted slots:**

- `USED` bit is 0
- If `FLAG_ORDERED_KEYS` is set, key bytes MUST remain unchanged across delete
- Otherwise, key/index bytes MAY be left unchanged or overwritten (implementation-defined)

---

## Buckets layout (hash index)

Open-addressed hash table with linear probing.

Each bucket is 16 bytes:

| Field | Type | Meaning |
|-------|------|---------|
| hash64 | u64 | hash of key bytes |
| slot_plus1 | u64 | 0=EMPTY, 0xFFFFFFFFFFFFFFFF=TOMBSTONE, else slot_id+1 |

Bucket states:

- EMPTY: `slot_plus1 == 0`
- TOMBSTONE: `slot_plus1 == 0xFFFFFFFFFFFFFFFF`
- FULL: otherwise; `slot_id = slot_plus1 - 1`

### Bucket count sizing

`bucket_count` is immutable after creation.

Implementations SHOULD size `bucket_count = nextPowerOfTwo(slot_capacity * 2)` to maintain load factor ≤ 0.5.

This ensures that even when all slots are live, the hash table remains efficient (average ~1.5 probes per lookup with linear probing at 50% load).

### Hash algorithm

The `hash_alg` field specifies the hash function:

- `0` = invalid (MUST reject)
- `1` = FNV-1a 64-bit

v1 uses **FNV-1a 64-bit** over the `key` bytes:

- offset basis: `14695981039346656037` (`0xcbf29ce484222325`)
- prime: `1099511628211` (`0x100000001b3`)

Note: FNV-1a is not cryptographically secure. Under adversarial keys, collision clustering can degrade performance; v1 assumes non-adversarial cache keys.

### Probe sequence

Given `h = hash64(key)` and `mask = bucket_count - 1`:

- start index: `i = h & mask`
- probe: `i = (i + 1) & mask` until found or an EMPTY bucket is encountered

Lookups MUST be bounded: if a probe visits `bucket_count` buckets without encountering EMPTY, the table violates the "at least one EMPTY bucket" invariant and is corrupt.

### Lookup semantics

On a FULL bucket, `hash64` is only a hint. Implementations MUST verify the candidate slot's key bytes against the requested key before returning a match.

If `hash64` matches but key bytes do not, the lookup MUST continue probing (hash collision).

If a bucket points to a slot_id out of range (`slot_id >= slot_highwater`) under a stable even generation, the file is corrupt.

### Delete semantics in buckets

Deleting a key MUST:

- set its bucket's `slot_plus1` to TOMBSTONE, and
- update `bucket_used/bucket_tombstones` accordingly

The `hash64` field in a TOMBSTONE bucket is ignored (it MAY be left unchanged).

### Rehashing

If `bucket_tombstones / bucket_count` exceeds a threshold (implementation-defined, e.g., 0.25), writers SHOULD rehash (rebuild the buckets from the live slots), resetting `bucket_tombstones` to 0.

Rehashing MUST occur during a write session (under the writer lock). Implementations MAY trigger rehashing automatically at commit time or provide a separate explicit rehash API.

---

## Sparse allocation and disk usage

slotcache files are typically created to their **full logical size** up front (based on `slot_capacity` and `bucket_count`) to keep the layout fixed and mmap-friendly.

To avoid consuming physical disk blocks up front, implementations SHOULD create the file as a **sparse file**:

- Implementations SHOULD size the file using `ftruncate` (or equivalent) and SHOULD NOT preallocate space by default
- Implementations MUST NOT eagerly write/initialize the entire slots or buckets sections on create

**When does the file "cost disk"?**

- The logical size (`ls -l`) is set immediately by `ftruncate`
- Physical disk usage (`du`) increases only for ranges that have been **written** (typically when dirty pages are written back)
