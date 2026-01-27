# slotcache WAL-first format (new v1)

## Goals

- **WAL-only commits**: commits append to a WAL region; the base snapshot is updated only by checkpoint.
- **Fast reads**: base slots + buckets remain contiguous for scan/lookup speed.
- **Crash safety**: committed WAL records are replayable; incomplete WAL tails are ignored.
- **Simple recovery**: WAL index + reader slots are rebuildable; base buckets/counters can be rebuilt from slots.
- **No per-read syscalls**: readers publish their snapshot in shared memory; locks are only used to claim reader slots.

## Naming conventions (used in this document)

- `*_offset` = absolute file byte offset
- `*_size` = size in bytes
- `*_count` = count of things
- `*_seq` = sequence number (txn/version; monotonically increasing)
- `read_seq` = per-operation snapshot sequence number (captured from `commit_seq`)

---

## File layout

```
┌──────────────────────────────────────────────────┐
│  Header (page-aligned, header_size)              │
├──────────────────────────────────────────────────┤
│  Base slots (snapshot data, page-aligned)        │
├──────────────────────────────────────────────────┤
│  Base buckets (snapshot index, page-aligned)     │
├──────────────────────────────────────────────────┤
│  WAL key index (page-aligned)                    │
├──────────────────────────────────────────────────┤
│  Reader slots (page-aligned)                
├──────────────────────────────────────────────────┤
│  WAL log (ring buffer, page-aligned)             │
└──────────────────────────────────────────────────┘
```
**Alignment:** All section offsets MUST be aligned to `page_size`. The header MUST be aligned to `page_size`. Internal records (slots, WAL records, index entries) MUST be 8-byte aligned.

---

## Header (fixed layout)

The header is a fixed layout at the start of the file. `header_size` MUST be a power of two, MUST be >= `page_size`, and SHOULD equal the OS page size at creation time (typically 4096 or 16384) when it can fit the header payload. If the header payload exceeds `page_size`, `header_size` MUST be the next power of two that can fit it.

All numeric fields are little-endian.

```
0x000: magic[4]              = "SLC1"                          # format magic
0x004: version u32           = 1                               # format version
0x008: header_size u32       (power of two, >= page_size)      # header bytes
0x00C: page_size u32         (power of two, >= 4096)           # layout alignment
0x010: key_size u32                                            # key length (bytes)
0x014: index_size u32                                          # index length (bytes)
0x018: slot_size u32                                           # slot record size
0x01C: hash_alg u32          (1 = FNV-1a 64-bit)               # hash algorithm id
0x020: flags u32                                               # format flags
0x024: reserved u32                                            # MUST be zero (padding)

0x028: user_version u64                                        # caller schema version
0x030: slot_capacity u64                                       # max slots
0x038: bucket_count u64                                        # hash buckets (power of two)
0x040: wal_index_size u64                                      # WAL index size (bytes)
0x048: reader_slot_count u32                                  # max concurrent processes
0x04C: reader_slot_size u32  (default 16)                      # reader slot size (bytes)
0x050: wal_size u64                                            # WAL region size (bytes, multiple of page_size)

0x058: slot_count u64                                      # allocated slots
0x060: base_live_count u64                                          # live slots (base)
0x068: base_bucket_used u64                                         # used buckets (base)
0x070: base_bucket_tombstones u64                                   # tombstones (base)

0x078: wal_head_offset u64           (excluded from header CRC)       # first uncheckpointed WAL record (abs offset)
0x080: wal_tail_offset u64           (excluded from header CRC)       # next WAL append offset
0x088: commit_seq u64            (excluded from header CRC)       # last committed transaction sequence number

0x090: base_generation u64           (excluded from header CRC)       # checkpoint seqlock (even=stable, odd=in-progress)
0x098: reader_pause u32       (excluded from header CRC)       # 0=reads allowed, 1=block new reads
0x09C: reader_slot_hint u32        (excluded from header CRC)       # best-effort starting slot index for Open

0x0A0: overlay_tail_key[key_size] (padded to 8-byte alignment)     # last overlay new-insert key (ordered inserts)
0x0A0+K: overlay_live_delta i64                                    # base_live_count delta from WAL overlay
0x0A8+K: state u32              (0=normal, 1=invalidated)      # cache state
0x0AC+K: header_crc32c u32      (CRC32-C, Castagnoli)          # header checksum
0x0B0+K: user_flags u64                                        # caller flags
0x0B8+K: user_data[1024]byte                                   # caller data

0x4B8+K..header_size-1: reserved (MUST be zero)                # future expansion
```

Where `K = align8(key_size)`.

### Header CRC rules

`header_crc32c` MUST be computed as CRC32-C over the entire header **with**:
- `header_crc32c` bytes zeroed,
- `wal_head_offset`, `wal_tail_offset`, `commit_seq`, `base_generation`, `reader_pause`, `reader_slot_hint`, `overlay_tail_key`, and `overlay_live_delta` bytes zeroed.

This allows runtime/WAL metadata to change without recomputing the header CRC.

### Value constraints

Let `K = align8(key_size)`.

- `page_size` MUST be a power of two between 4096 and 65536.
- `header_size` MUST be a power of two, MUST be >= `page_size`, and MUST be >= `0x4B8 + K`.
- `header_size` MUST be <= 65536 (key_size MUST fit within this bound).
- `wal_size` MUST be a positive multiple of `page_size`.
- Section offsets are derived and MUST be aligned to `page_size`.
- The derived layout MUST fit in the file length.

### Header flags

`flags` is a bitset owned by the format. Unknown flag bits MUST be rejected as incompatible.

- `FLAG_ORDERED_KEYS = 1 << 0`
  - Base slots are globally sorted by key bytes (slot-id order).
  - Writer MUST enforce append-only ordered inserts.
  - Tombstoned slots MUST preserve key bytes.
  - Ordered range scans are enabled.

---

## Derived layout

All section offsets are derived from header sizes and counts. Define:

```
alignPage(x) = ((x + page_size - 1) / page_size) * page_size
```

Then:

```
slots_offset        = header_size
buckets_offset      = alignPage(slots_offset + slot_capacity*slot_size)
wal_index_offset    = alignPage(buckets_offset + bucket_count*16)
reader_slots_offset   = alignPage(wal_index_offset + wal_index_size)
wal_offset          = alignPage(reader_slots_offset + reader_slot_count*reader_slot_size)
wal_region_end_offset   = wal_offset + wal_size
```

All derived offsets MUST be aligned to `page_size`, and `wal_region_end_offset` MUST fit within the file length.

---

## Slots section

Slots are fixed-size, 8-byte aligned records. Layout:

```
slot_size = align8( meta(8) + key_size + key_pad + revision(8) + index_size )
```

Fields:
- `meta u64` (bit 0 = USED)
- `key[key_size]`
- `revision i64`
- `index[index_size]`

Slots are append-only: `slot_count` grows monotonically. Deletes clear the USED bit (tombstone).

---

## Buckets section (base index)

Open-addressed hash table:

```
struct Bucket {
  hash64    u64
  slot_id_plus1 u64   // 0=EMPTY, 0xFFFFFFFFFFFFFFFF=TOMBSTONE, else slot_id+1
}
```

`base_bucket_used` and `base_bucket_tombstones` describe **base snapshot** only (not WAL overlay).

Hash collisions:
- `hash64` is a hint only.
- Readers MUST verify the candidate slot’s key bytes match the requested key; on mismatch they MUST continue probing.

---

## WAL Index section

Open-addressed hash table mapping `key → WAL record offset` for the latest record in WAL order up to the current `wal_tail_offset`.

```
struct WalIndexEntry {
  hash64           u64
  record_offset_plus1 u64  // 0=EMPTY, 0xFFFFFFFFFFFFFFFF=TOMBSTONE, else record_offset+1
}
```

- `record_offset` is an absolute file offset into the WAL region.
- `hash64` is a hint only. On lookup, readers MUST verify the referenced WAL record’s key bytes match the requested key; on mismatch (hash collision) they MUST continue probing.
- The WAL index always points to the **latest** record for a key. Snapshot reads MUST follow the per-record `prev_record_offset_plus1` chain to reach the latest record with `txn_seq <= read_seq`.
- If a WAL index entry points to an invalid WAL record (out of bounds, CRC mismatch, wrong record type), the entry MUST be treated as invalid (continue probing). Implementations SHOULD rebuild the WAL index (or fall back to a WAL scan) if this occurs.
- WAL index is **rebuildable** by scanning the WAL; it is not authoritative.

---

## Reader Slots section (per-process tracking)

Reader slots are **process-owned** entries used to publish the minimum active reader snapshot (`read_seq_min`) for checkpoint safety.

### On-disk (mmapped) slot

```
struct ReaderSlot {
  read_seq_min    u64  // MIN read_seq across active reads in this process; 0 if none
  active_reads    u32  // active read count in this process
  reserved        u32
}
```

### Slot ownership via byte-range lock

- Each reader slot corresponds to a **lock byte** at: `reader_slots_offset + i*reader_slot_size`.
- A process MUST claim at most one reader slot.
- To claim slot `i`, the process MUST acquire an **exclusive (WRLCK)** lock on that lock byte.
- The process MUST hold this lock for the lifetime of its participation in the database (i.e. until the last handle to the file in that process closes).
- A slot’s contents MUST be treated as valid only if the slot lock is currently held by some process.
  - Checkpointers MUST determine which reader slots are live vs stale. Implementations MAY do this by probing locks (non-blocking) or by querying lock state (e.g., POSIX `F_GETLK`).

**Locking note:** Implementations MUST account for the semantics of their locking primitive. For example, POSIX `fcntl` byte-range locks are per-process; closing *any* file descriptor for the database file may release all record locks for that process. Implementations MUST ensure the reader slot lock is held continuously for the intended lifetime (e.g., by keeping a dedicated FD open and sharing it across all handles in the process).

**Probing note:** On per-process lock APIs (e.g., POSIX `fcntl` locks), “probing” cannot distinguish an *unlocked* byte from a byte already locked by the current process (a non-blocking lock attempt may succeed in both cases). Checkpointers that run in the same process as readers MUST treat their own claimed reader slot as live and MUST NOT use probe success alone to classify a slot as stale.

### Open-time slot selection hint

`reader_slot_hint` is a best-effort starting index for claiming a slot. It is excluded from header CRC and does not require `msync`.

Typical algorithm:
- `start = fetch_add(reader_slot_hint, 1) % reader_slot_count`
- probe locks in ring order starting at `start`.

---

## WAL Log section

The WAL region is a **ring buffer** of records. `wal_tail_offset` is the next write position; `wal_head_offset` is the first uncheckpointed record.

### Ring order and `[wal_head_offset, wal_tail_offset)` notation

When this document refers to scanning the WAL “from `wal_head_offset` to `wal_tail_offset`” or checking whether an offset lies within ``[wal_head_offset, wal_tail_offset)`` it refers to the **half-open interval in WAL (ring) order**, not necessarily a simple numeric range.

Let `wal_region_end_offset = wal_offset + wal_size`.

- If `wal_head_offset <= wal_tail_offset`, then ``[wal_head_offset, wal_tail_offset)`` is the numeric range `[wal_head_offset, wal_tail_offset)`.
- If `wal_head_offset > wal_tail_offset` (wrapped), then ``[wal_head_offset, wal_tail_offset)`` is the union of numeric ranges `[wal_head_offset, wal_region_end_offset)` and `[wal_offset, wal_tail_offset)`.

Offsets outside the WAL region MUST be treated as out-of-bounds.

### Record header (32 bytes)

```
struct WalRecordHeader {
  record_size          u32  // total length including header + payload + padding
  crc32c           u32  // CRC32-C over header (crc=0) + record bytes after the header (i.e. the full payload including padding)
  txn_seq           u64  // transaction sequence number (monotonically increasing)
  prev_record_offset_plus1 u64 // previous WAL record for same key (0 if none or outside current live WAL window)
  type             u8   // PUT, DEL, USERHDR, COMMIT, PAD
  flags            u8
  reserved         u16
  reserved2        u32
}
```

Records MUST be padded to 8-byte alignment; `record_size` includes padding.

### Record types

- **PUT**: `key[key_size] + revision(i64) + index[index_size]`
- **DEL**: `key[key_size]`
- **USERHDR**: `user_flags u64 + user_data[1024]`
- **COMMIT**: no payload
- **PAD**: filler to end-of-region when wrapping

Transaction sequence numbers:
- For PUT/DEL/USERHDR/COMMIT records, `txn_seq` MUST equal the transaction's sequence number.
- Transaction sequence numbers MUST be **monotonically increasing** for the lifetime of the file and MUST NOT be reset.
  - `commit_seq` stores the latest committed transaction sequence number.
- Writers MUST ensure that as records appear in WAL order (including across PAD wrap), `txn_seq` values are non-decreasing.
- PAD records MUST use `txn_seq = commit_seq` at the time the PAD record is written. PAD records do not represent a commit.

`prev_record_offset_plus1` MUST be zero for USERHDR, COMMIT, and PAD records.

For PUT/DEL records, writers SHOULD set `prev_record_offset_plus1` to the previous WAL record for the same key if that record's offset is within the live WAL window at the time the record is written (`[wal_head_offset, wal_tail_offset)`); otherwise it MUST be zero.

Readers MUST treat `prev_record_offset_plus1` as zero (end of chain) if it points outside the current live WAL window (i.e. not within `[wal_head_offset, wal_tail_offset)` in ring order) or outside the WAL region.

### Validity

A record is valid if:
- `record_size >= sizeof(WalRecordHeader)`
- `record_size` is 8-byte aligned
- `record_size` fits within the WAL region (i.e. the record does not cross `wal_region_end_offset`; writers MUST use PAD/implicit PAD to wrap)
- `type` is one of: PUT, DEL, USERHDR, COMMIT, PAD
- `record_size` matches the expected size for the record type:
  - PUT: `record_size == align8(sizeof(WalRecordHeader) + key_size + 8 + index_size)`
  - DEL: `record_size == align8(sizeof(WalRecordHeader) + key_size)`
  - USERHDR: `record_size == align8(sizeof(WalRecordHeader) + 8 + 1024)`
  - COMMIT: `record_size == align8(sizeof(WalRecordHeader))` (32)
  - PAD: `record_size == wal_region_end_offset - record_offset` (it fills to end-of-region)
- CRC32C matches

The last valid **COMMIT** record defines `commit_seq` for recovery.

---

## Alignment and sizing rules

- All section offsets MUST be aligned to `page_size`.
- Internal records (slots, WAL records, index entries) MUST be 8-byte aligned.
- WAL region size MUST be a multiple of `page_size`.
- `wal_head_offset`, `wal_tail_offset` are absolute offsets within `[wal_offset, wal_region_end_offset)`.

---

## Notes on locality

- Slots + buckets remain contiguous for scan locality.
- WAL index + reader slots are small and hot; place them adjacent.
- WAL log is at the end for sequential append and easy growth.

---

## Operational semantics

### Base generation (`base_generation`) and read pausing (`reader_pause`)

- `base_generation` is a global seqlock/counter for checkpoint base mutations:
  - **even**: base snapshot is stable
  - **odd**: checkpoint is mutating/rebuilding base
- `reader_pause` is a best-effort flag used to block **new** reads from starting while a checkpoint attempts to drain active readers.

Readers MUST use `base_generation` to detect overlap with checkpointing and MUST NOT return results that overlap a checkpoint.

### Reader snapshot protocol (per operation)

Each read operation (Get/Scan/Len/UserHeader/etc.) uses its own snapshot `read_seq`.

**StartRead:**
1. If `reader_pause==1`, the read MUST retry or return `ErrBusy`.
2. Read `g1 := base_generation`. If `g1` is odd, the read MUST retry or return `ErrBusy`.
3. Read `read_seq := commit_seq` (the snapshot sequence number for this operation).
4. Publish activity in the process’ local table and update `ReaderSlot`:
   - increment `active_reads`
   - recompute `read_seq_min` for this process and store it
5. Re-check `reader_pause` and `base_generation`:
   - if `reader_pause==1` or `base_generation != g1` (or now odd), undo the publication and retry/return `ErrBusy`.

**During long reads (recommended):** implementations SHOULD periodically check `base_generation` in long loops (e.g. scans) and abort early if it changes.

**EndRead (before returning results):**
1. Read `g2 := base_generation`. If `g2 != g1` or `g2` is odd, the read MUST discard results and return `ErrBusy`.
2. Clear the local slot, decrement `active_reads`, and update `read_seq_min`.

### WAL overlay rules

For a given key and snapshot `read_seq`:

- The WAL index points to the **latest** record for a key. If that record has `txn_seq > read_seq`, readers MUST follow the `prev_record_offset_plus1` chain until a record with `txn_seq <= read_seq` is found or the chain ends.
- If the latest visible record is `DEL`, the key is treated as absent.
- If no WAL record with `txn_seq <= read_seq` exists (or the chain ends / points outside the current live WAL window), readers MUST fall back to the base snapshot.
- The WAL index is non-authoritative; if missing or corrupt, it MUST be rebuilt by scanning the WAL.

### Commit (WAL-only)

Commits append to the WAL and do **not** modify the base snapshot.

1. Acquire the writer lock.
2. Ensure WAL space for the entire commit (all records + COMMIT). If the remaining WAL region cannot fit the commit, write a PAD record and wrap to `wal_offset`. If fewer than `sizeof(WalRecordHeader)` bytes remain, the writer MUST wrap without writing PAD (the trailing bytes are unused).
3. If space is still insufficient, attempt checkpoint. If still insufficient, return `ErrBusy`/`ErrFull`.
4. Reserve `txn_seq = commit_seq + 1` for this commit; all records MUST use this `txn_seq`.
5. Append one WAL record per buffered op (PUT/DEL/USERHDR):
   - For PUT/DEL, set `prev_record_offset_plus1` to the current WAL index entry for that key if it points within the current live WAL window (`[wal_head_offset, wal_tail_offset)`), else `0`.
6. Append a COMMIT record.
7. Update `wal_tail_offset` to the next append offset (the byte after the COMMIT record).
8. Update WAL index entries to point at the latest record for each key.
9. Update `overlay_live_delta` if present (net inserts/deletes in WAL overlay).
10. Publish the commit by storing `commit_seq = txn_seq` (visibility barrier for readers).
11. **Do not** modify base slots/buckets/counters.

**Ordered-keys mode (FLAG_ORDERED_KEYS):**

- A PUT for a **new key** (not present in base and not present in WAL) MUST satisfy
  `key >= max(base_tail_key, overlay_tail_key)`, else `ErrOutOfOrderInsert`.
- `overlay_tail_key` MUST be updated on each new insert.
- Updates to existing keys do not affect ordering.
- Within a single commit, new keys MUST be appended in non-decreasing key order.

`base_tail_key` is the key bytes in slot `slot_count-1` (tombstones preserved). If `slot_count == 0`, `base_tail_key` is an all-zero key.

### Read operations

#### Get (point lookup)
1. StartRead → snapshot `read_seq`.
2. Consult WAL index to get the latest record for the key. If present:
   - If `txn_seq > read_seq`, follow `prev_record_offset_plus1` until `txn_seq <= read_seq` or the chain ends.
   - If a visible record is found: return PUT or miss for DEL.
3. If no visible WAL record exists, fall back to base buckets/slots.
4. EndRead.

#### Scan (unordered)
1. StartRead → snapshot `read_seq`.
2. Build an overlay map of the latest WAL record per key with `txn_seq <= read_seq`
   (by scanning WAL from `wal_head_offset` to `wal_tail_offset`, ignoring records with `txn_seq > read_seq`).
3. Scan base slots sequentially:
   - If overlay has DEL → skip.
   - If overlay has PUT → return WAL value.
   - Else → return base value.
4. Track keys seen in base; after the base scan, emit WAL-only inserts not seen in base, in WAL order.
5. EndRead.

#### Ordered scans (FLAG_ORDERED_KEYS)
- Base slots are globally ordered.
- New WAL inserts are enforced monotonic via `overlay_tail_key`, so WAL-only inserts are ordered and `>= base_tail_key`.
- Ordered scans MUST emit base slots in order with WAL overlay applied, then append WAL-only inserts in WAL order.

#### Len / UserHeader / Generation
- `Len()` SHOULD return `base_live_count + overlay_live_delta` (if `overlay_live_delta` is used).
- `UserHeader()` returns the latest USERHDR record `<= read_seq`, else the base header values.
- `Generation()` MAY return `commit_seq` as a cheap change detector / version.
  - `commit_seq` is monotonically increasing, so callers can use it for "did anything change?" checks.

### Checkpoint

Checkpoint moves WAL changes into the base snapshot.

**Safety rule:** A checkpoint MUST NOT advance past any active reader snapshot.

#### Recommended checkpoint algorithm (pause + drain)
1. Acquire the writer lock.
2. Set `reader_pause=1` to block new readers.
3. Attempt to drain active readers by waiting for all *live* reader slots to report `active_reads==0`.
   - A slot is considered live iff its lock byte is currently held by some process.
   - Implementations SHOULD bound this wait and return `ErrBusy` if readers do not drain.
4. Begin checkpoint base mutation:
   - set `base_generation` to a new odd value
   - `msync` the header (barrier: ensures base_generation odd is durable before base writes)
5. Set `ckpt_end_offset = wal_tail_offset` and apply WAL records in order from `wal_head_offset` through `ckpt_end_offset`.
6. Rebuild buckets and counters from slots (clear all buckets, reinsert live slots, recompute counters).
7. `msync` base ranges (slots + buckets).
8. Publish checkpoint:
   - advance `wal_head_offset` to the record after `ckpt_end_offset` (if WAL empty, `wal_head_offset == wal_tail_offset`)
   - update `overlay_live_delta` to reflect only uncheckpointed WAL records
   - in ordered-keys mode, if WAL becomes empty, set `overlay_tail_key = base_tail_key`
   - set `base_generation` to a new even value
   - clear `reader_pause=0`
   - recompute and store `header_crc32c` (required if any CRC-covered header bytes changed during the checkpoint)
   - `msync` the header

#### Non-blocking checkpoint (optional)
Implementations MAY apply only up to the oldest active reader snapshot:

- determine `safe_seq = min(read_seq_min across live reader slots with active_reads>0)`
- determine `ckpt_end_offset` as the byte offset immediately after the last valid COMMIT record with `txn_seq <= safe_seq` (if none, `ckpt_end_offset = wal_head_offset` and the checkpoint is a no-op)
- apply only whole WAL transactions with `txn_seq <= safe_seq` by scanning/applying records in `[wal_head_offset, ckpt_end_offset)`

Note: even a non-blocking checkpoint mutates base pages; readers MUST use `base_generation` to avoid returning results that overlap the mutation.

### Recovery / Open

On Open:

- Validate header CRC and layout.
- If `base_generation` is odd, a checkpoint was interrupted. Implementations SHOULD run a checkpoint (or rebuild base indexes/counters) before serving reads.
- Scan WAL from `wal_head_offset` forward, validating records.
  - The scan MUST treat PAD records as wrap markers (advance to `wal_offset` and continue).
  - If fewer than `sizeof(WalRecordHeader)` bytes remain to the end of the WAL region, the scan MUST wrap to `wal_offset` and continue (equivalent to an implicit PAD).
  - The scan MUST stop at the first invalid record (bad length/alignment/CRC/type), or when `txn_seq` decreases compared to the previously scanned record (indicating reclaimed old ring contents), or after scanning `wal_size` bytes (full-circle guard).
- The last valid COMMIT record encountered by this scan defines `commit_seq`; `wal_tail_offset` MUST be set to the byte offset immediately after that COMMIT record.
  - Rationale: implementations may not `msync` the header on every commit, so persisted `commit_seq`/`wal_tail_offset` may be stale; the WAL scan reconstructs the true committed tail.
- Rebuild the WAL index by scanning WAL from `wal_head_offset` to `wal_tail_offset`.
- If `FLAG_ORDERED_KEYS` is set, implementations MUST reconstruct `overlay_tail_key` by replaying committed WAL records in WAL order and tracking WAL-only **new inserts** (PUTs that transition a key from absent→present relative to the base snapshot and prior WAL ops). If there are no committed WAL new inserts, `overlay_tail_key` MUST be set to `base_tail_key`.
- If `overlay_live_delta` is used, implementations MUST reconstruct it from the committed WAL overlay (net effect at `commit_seq` relative to the base snapshot).
- Reader slot contents are ignored unless their slot lock byte is held by some process.

### Durability & writeback

- **WritebackSync:** Commit MUST `msync` the WAL after writing COMMIT.
  - Implementations MAY avoid `msync`ing the header on every commit (to avoid extra overhead). In that case, persisted `commit_seq`/`wal_tail_offset` are best-effort only and recovery MUST reconstruct them via WAL scan.
- **Checkpoint:** MUST always use `msync` barriers as described above.
- **WritebackNone:** Commits are atomic but not durable; power loss may drop WAL tail.
  Base remains consistent as of the last successful checkpoint.

### WAL wrap & full handling

- If a record does not fit to the end of the WAL region, the writer MUST wrap to `wal_offset`. If there is enough space to write a PAD record (at least `sizeof(WalRecordHeader)` bytes), it MUST write a PAD record to consume the remaining bytes; otherwise the trailing bytes are unused (implicit PAD).
- If there is still insufficient space, the writer MUST attempt checkpoint; if space cannot be freed (active readers), return `ErrBusy`/`ErrFull`.
