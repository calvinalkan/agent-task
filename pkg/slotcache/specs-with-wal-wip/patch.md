

 ┌──────────────────────────────────────────────────┐
 │  Header (page-aligned, header_size)              │
 ├──────────────────────────────────────────────────┤
 │  Base slots (snapshot data, page-aligned)        │
 ├──────────────────────────────────────────────────┤
 │  Base buckets (snapshot index, page-aligned)     │
 ├──────────────────────────────────────────────────┤
-│  WAL key index (page-aligned)                    │
+│  WAL key index (optional, page-aligned)          │
 ├──────────────────────────────────────────────────┤
 │  Reader slots (page-aligned)                
 ├──────────────────────────────────────────────────┤
 │  WAL log (ring buffer, page-aligned)             │
 └──────────────────────────────────────────────────┘

---

 - `wal_size` MUST be a positive multiple of `page_size`.
+- `wal_index_size` MAY be zero (WAL index section absent/unused). If `wal_index_size == 0`,
+  implementations MUST treat the WAL index as disabled and MUST NOT rely on it for correctness.
 - Section offsets are derived and MUST be aligned to `page_size`.
 - The derived layout MUST fit in the file length.

 ---
 
 +### Runtime marker fields (non-durable hints)
 +
 +The header fields `commit_seq`, `wal_tail_offset`, `wal_head_offset`, `overlay_tail_key`,
 +and `overlay_live_delta` are runtime metadata that MAY be updated frequently and are excluded
 +from `header_crc32c` for performance. These fields are **non-authoritative on disk**: after a crash,
 +their persisted values MAY be stale or torn.
 +
 +Implementations MAY treat the pair (`commit_seq`, `wal_tail_offset`) as a **best-effort marker**
 +for where the last committed transaction ended. Implementations MUST validate any such marker
 +against WAL contents before using it as an optimization (e.g. as a scan start hint).
 +
 +Validation rule (recommended):
 +- Let `COMMIT_SIZE = align8(sizeof(WalRecordHeader))` (32).
 +- Let `end = wal_region_end_offset`.
 +- Define `commit_off_hint = (wal_tail_offset == wal_offset ? end : wal_tail_offset) - COMMIT_SIZE`.
 +- The marker is valid iff the record at `commit_off_hint` parses as a valid COMMIT record and has
 +  `txn_seq == commit_seq`.
 +
 +If validation fails, the marker MUST be ignored and recovery MUST fall back to WAL scanning.

---

 ### WAL wrap & full handling
 
 - If a record does not fit to the end of the WAL region, the writer MUST wrap to `wal_offset`. If there is enough space to write a PAD record (at least `sizeof(WalRecordHeader)` bytes), it MUST write a PAD record to consume the remaining bytes; otherwise the trailing bytes are unused (implicit PAD).
+- If writing a record causes the next append position to equal `wal_region_end_offset`, the writer MUST wrap and set the next append position to `wal_offset`. In this case, `wal_tail_offset` MUST be stored as `wal_offset` (never as `wal_region_end_offset`).
 - If there is still insufficient space, the writer MUST attempt checkpoint; if space cannot be freed (active readers), return `ErrBusy`/`ErrFull`.

 ---
 
 - The scan MUST treat PAD records as wrap markers (advance to `wal_offset` and continue).
 - If fewer than `sizeof(WalRecordHeader)` bytes remain to the end of the WAL region, the scan MUST wrap to `wal_offset` and continue (equivalent to an implicit PAD).
+- If the scan cursor becomes exactly `wal_region_end_offset`, the scan MUST wrap to `wal_offset` and continue (equivalent to an implicit PAD of length 0).

 ---
 
 ## WAL Index section
 
 Open-addressed hash table mapping `key → WAL record offset` for the latest record in WAL order up to the current `wal_tail_offset`.
 
+This section is **optional**. If `wal_index_size == 0`, the WAL index is disabled/absent.
+
 struct WalIndexEntry {
   hash64           u64
   record_offset_plus1 u64  // 0=EMPTY, 0xFFFFFFFFFFFFFFFF=TOMBSTONE, else record_offset+1
 }
 
 - `record_offset` is an absolute file offset into the WAL region.
 - `hash64` is a hint only. On lookup, readers MUST verify the referenced WAL record’s key bytes match the requested key; on mismatch (hash collision) they MUST continue probing.
-- The WAL index always points to the **latest** record for a key. Snapshot reads MUST follow the per-record `prev_record_offset_plus1` chain to reach the latest record with `txn_seq <= read_seq`.
+- If present and maintained, the WAL index SHOULD point to the **latest** record for a key in WAL order.
+  Readers MUST treat the WAL index as a **hint**, not an authority:
+  - Readers MUST validate the referenced WAL record (bounds/type/size/CRC/key match) before using it.
+  - If the entry is missing, stale, or invalid, readers MUST fall back to an O(WAL) scan (or to base).
+  - Implementations MAY additionally follow `prev_record_offset_plus1` chains to find a visible version for a snapshot.
 - If a WAL index entry points to an invalid WAL record (out of bounds, CRC mismatch, wrong record type), the entry MUST be treated as invalid (continue probing).
-- WAL index is **rebuildable** by scanning the WAL; it is not authoritative.
+- The WAL index is **rebuildable** by scanning the committed WAL window; it is not authoritative.
+
+### WAL index maintenance policy (implementation choice)
+
+Implementations MAY expose a runtime policy knob for WAL index handling, for example:
+- **Rebuild-on-Open**: scan committed WAL and rebuild the index (recommended for long-lived processes).
+- **Hint-only**: do not rebuild; use existing entries as hints; allow fallbacks (recommended for short-lived CLI).
+- **Disabled**: ignore the index entirely (as if `wal_index_size == 0`).
+
+Regardless of policy, correctness MUST NOT depend on the WAL index.

---

 ### Commit (WAL-only)
 
 Commits append to the WAL and do **not** modify the base snapshot.
 
 1. Acquire the writer lock.
 2. Ensure WAL space for the entire commit (all records + COMMIT). If the remaining WAL region cannot fit the commit, write a PAD record and wrap to `wal_offset`. If fewer than `sizeof(WalRecordHeader)` bytes remain, the writer MUST wrap without writing PAD (the trailing bytes are unused).
 3. If space is still insufficient, attempt checkpoint. If still insufficient, return `ErrBusy`/`ErrFull`.
 4. Reserve `txn_seq = commit_seq + 1` for this commit; all records MUST use this `txn_seq`.
 5. Append one WAL record per buffered op (PUT/DEL/USERHDR):
-   - For PUT/DEL, set `prev_record_offset_plus1` to the current WAL index entry for that key if it points within the current live WAL window (`[wal_head_offset, wal_tail_offset)`), else `0`.
+   - For PUT/DEL, implementations MAY set `prev_record_offset_plus1` to the previous WAL record for the same key if known and if it points within the current live WAL window (`[wal_head_offset, wal_tail_offset)`), else `0`.
+     (If the WAL index is disabled/absent, writers MAY still maintain a private in-memory map for this purpose; if unknown, set to `0`.)
 6. Append a COMMIT record.
-7. Update `wal_tail_offset` to the next append offset (the byte after the COMMIT record).
-8. Update WAL index entries to point at the latest record for each key.
-9. Update `overlay_live_delta` if present (net inserts/deletes in WAL overlay).
-10. Publish the commit by storing `commit_seq = txn_seq` (visibility barrier for readers).
+7. Compute the next append position (byte after COMMIT). If this equals `wal_region_end_offset`, wrap it to `wal_offset`.

+   - Implementations MUST flush the page-aligned WAL byte ranges that were dirtied by this transaction (including the COMMIT record) using `msync`/`fdatasync`/equivalent.
+   - This durability barrier does not require syncing the header; header fields remain non-durable hints.
+9. Update `wal_tail_offset` to the computed next append offset.
+10. If `wal_index_size > 0` and WAL index usage is enabled, update WAL index entries to point at the latest record for each key.
+11. Update `overlay_live_delta` (net inserts/deletes in WAL overlay).
+12. Publish the commit by storing `commit_seq = txn_seq` (visibility barrier for readers).
+13. Implementations MAY optionally call `msync(MS_ASYNC)` (or equivalent) on the header page to encourage marker writeback,
+    but MUST NOT rely on it for correctness.
 11. **Do not** modify base slots/buckets/counters.

---

 ### Durability & writeback
 
-- **WritebackSync:** Commit MUST `msync` the WAL after writing COMMIT.
-  - Implementations MAY avoid `msync`ing the header on every commit (to avoid extra overhead). In that case, persisted `commit_seq`/`wal_tail_offset` are best-effort only and recovery MUST reconstruct them via WAL scan.
-- **Checkpoint:** MUST always use `msync` barriers as described above.
-- **WritebackNone:** Commits are atomic but not durable; power loss may drop WAL tail.
-  Base remains consistent as of the last successful checkpoint.
+Writeback behavior is an **implementation/runtime policy** (e.g. per-handle or per-commit), similar in spirit to a database “pragma”.
+It is not a required persistent format property. Recovery MUST be correct regardless of the writeback policy used previously.
+
+- **WritebackSync:** A successful commit MUST ensure the WAL bytes for that commit are durable (via `msync`/`fdatasync`/equivalent) after writing the COMMIT record and before returning success.
+  Implementations MAY avoid `msync`ing the header on every commit. In that case, persisted header marker fields
+  (`commit_seq`, `wal_tail_offset`, `overlay_tail_key`, `overlay_live_delta`) are best-effort hints only; recovery MUST reconstruct correctness-critical state by WAL scanning.
+
+- **Checkpoint:** MUST always use the `msync` barriers as described in the checkpoint protocol (base_generation odd barrier; base pages flush; then header publish).
+
+- **WritebackNone:** Commits are atomic with respect to concurrent operations in a running system, but provide **no durability guarantee** across power loss or OS crash.
+  After a crash/power loss, any subset of the most recent transactions may be lost.
+  **Recovery MUST NOT replay partial transactions:** only transactions whose COMMIT record is present within the recovered valid WAL prefix are applied.
+  (This relies on the recovery scan stopping at the first invalid record and not skipping invalid bytes to search for later commits.)

---

 ### Recovery / Open
 
 On Open:
 
-- Validate header CRC and layout.
-- If `base_generation` is odd, a checkpoint was interrupted. Implementations SHOULD run a checkpoint (or rebuild base indexes/counters) before serving reads.
-- Scan WAL from `wal_head_offset` forward, validating records.
-  - The scan MUST treat PAD records as wrap markers (advance to `wal_offset` and continue).
-  - If fewer than `sizeof(WalRecordHeader)` bytes remain to the end of the WAL region, the scan MUST wrap to `wal_offset` and continue (equivalent to an implicit PAD).
-  - The scan MUST stop at the first invalid record (bad length/alignment/CRC/type), or when `txn_seq` decreases compared to the previously scanned record (indicating reclaimed old ring contents), or after scanning `wal_size` bytes (full-circle guard).
-- The last valid COMMIT record encountered by this scan defines `commit_seq`; `wal_tail_offset` MUST be set to the byte offset immediately after that COMMIT record.
-  - Rationale: implementations may not `msync` the header on every commit, so persisted `commit_seq`/`wal_tail_offset` may be stale; the WAL scan reconstructs the true committed tail.
-- Rebuild the WAL index by scanning the WAL from `wal_head_offset` to `wal_tail_offset`.
-- If `FLAG_ORDERED_KEYS` is set, implementations MUST reconstruct `overlay_tail_key` by replaying committed WAL records in WAL order and tracking WAL-only **new inserts** (PUTs that transition a key from absent→present relative to the base snapshot and prior WAL ops). If there are no committed WAL new inserts, `overlay_tail_key` MUST be set to `base_tail_key`.
-- If `overlay_live_delta` is used, implementations MUST reconstruct it from the committed WAL overlay (net effect at `commit_seq` relative to the base snapshot).
-- Reader slot contents are ignored unless their slot lock byte is held by some process.
+
+- Validate header CRC and layout.
+
+- If `base_generation` is odd, a checkpoint was interrupted. Implementations MUST complete checkpoint recovery
+  (or rebuild base indexes/counters) before serving reads/writes, and MUST finish with `base_generation` even.
+
+#### Replay policy (implementation choice)
+
+Implementations MAY expose a runtime replay policy for Open. Two useful policies are:
+
+- **TailReplay:** Perform the minimum WAL replay required for correctness (recover committed tail and reconstruct required overlay metadata),
+  but skip optional rebuild work such as rebuilding the WAL index.
+- **FullReplay:** Perform the same correctness replay as TailReplay, and additionally rebuild optional structures (e.g. WAL index) for faster steady-state reads.
+
+Regardless of policy, the required correctness outcomes below MUST hold.
+
+#### Required correctness outcomes (MUST)
+
+After Open completes, implementations MUST have correct in-memory header values for:
+- `commit_seq` (last committed transaction sequence present in WAL)
+- `wal_tail_offset` (byte after the last committed COMMIT record; wrapped to `wal_offset` if it equals `wal_region_end_offset`)
+- `overlay_tail_key` (if `FLAG_ORDERED_KEYS` is set)
+- `overlay_live_delta` (if used by the implementation)
+- `reader_pause` MUST be 0 (unless Open returns `ErrBusy`)
+
+#### WAL tail discovery scan
+
+Open MUST discover the committed WAL tail by scanning WAL records in ring order and validating them:
+- The scan MUST treat PAD records as wrap markers (advance to `wal_offset` and continue).
+- If fewer than `sizeof(WalRecordHeader)` bytes remain to the end of the WAL region, the scan MUST wrap to `wal_offset` and continue (implicit PAD).
+- If the scan cursor becomes exactly `wal_region_end_offset`, the scan MUST wrap to `wal_offset` and continue.
+- The scan MUST stop at the first invalid record (bad length/alignment/CRC/type), or when `txn_seq` decreases compared to the previously scanned record
+  (indicating reclaimed old ring contents), or after scanning `wal_size` bytes (full-circle guard).
+- Implementations MUST NOT skip invalid records/bytes to search for later COMMITs. (Skipping invalid bytes can violate crash atomicity.)
+
+The last valid COMMIT record encountered by this scan defines the recovered `commit_seq`.
+`wal_tail_offset` MUST be set to the byte offset immediately after that COMMIT record (wrapping to `wal_offset` if needed).
+
+Rationale: implementations may not `msync` the header on every commit, so persisted header marker fields may be stale; WAL scanning reconstructs the true committed tail.
+
+#### Overlay reconstruction (MUST)
+
+After the committed tail is known, implementations MUST reconstruct overlay metadata from the committed WAL window:
+
+- If `FLAG_ORDERED_KEYS` is set, implementations MUST reconstruct `overlay_tail_key` by replaying committed WAL records in WAL order and tracking WAL-only **new inserts**
+  (PUTs that transition a key from absent→present relative to the base snapshot and prior WAL ops). If there are no committed WAL new inserts,
+  `overlay_tail_key` MUST be set to `base_tail_key`.
+
+- If `overlay_live_delta` is used, implementations MUST reconstruct it from the committed WAL overlay (net effect at recovered `commit_seq` relative to the base snapshot).
+
+Note: Implementations MAY compute these during the tail-discovery scan (e.g. by buffering per-txn until COMMIT) or in a second pass over the committed window;
+the required outcomes above are what matters.
+
+#### WAL index handling (optional)
+
+If `wal_index_size > 0`, the WAL index MAY be handled according to a runtime policy:
+- In **FullReplay**, implementations SHOULD rebuild the WAL index by scanning the committed WAL window and inserting the latest record offset per key.
+- In **TailReplay**, implementations MAY skip rebuilding the WAL index; any existing entries are treated as hints and MUST be validated on use.
+
+If `wal_index_size == 0`, the WAL index is disabled and MUST NOT be used.
+
+#### Locking requirements during Open
+
+Open-time replay that **publishes** reconstructed shared metadata (e.g., writing `commit_seq`, `wal_tail_offset`, `overlay_tail_key`,
+`overlay_live_delta`, rebuilding the shared WAL index, or performing checkpoint recovery) MUST be serialized with commits/checkpoints using the writer lock.
+
+Implementations MAY perform an optimistic read-only scan without holding the writer lock, but MUST revalidate under the writer lock
+before writing reconstructed values into the shared header (and MUST NOT decrease `commit_seq`).
+
+#### Reader slots
+
+Reader slot contents are ignored unless their slot lock byte is held by some process.

---

 ### WAL overlay rules
 
 For a given key and snapshot `read_seq`:
+
+- The WAL index is optional and non-authoritative. If the WAL index is disabled (`wal_index_size == 0`) or not available/rebuilt,
+  point lookups MUST fall back to scanning the committed WAL window (O(WAL)) and/or the base snapshot.
+  If the WAL index is present, readers MUST validate index hits and MUST fall back to scan/base on any mismatch.
 
 - The WAL index points to the **latest** record for a key. If that record has `txn_seq > read_seq`, readers MUST follow the `prev_record_offset_plus1` chain until a record with `txn_seq <= read_seq` is found or the chain ends.
 - If the latest visible record is `DEL`, the key is treated as absent.
 - If no WAL record with `txn_seq <= read_seq` exists (or the chain ends / points outside the current live WAL window), readers MUST fall back to the base snapshot.
 - The WAL index is non-authoritative; if missing or corrupt, it MUST be rebuilt by scanning the WAL.

 
---

- - The WAL index is non-authoritative; if missing or corrupt, it MUST be rebuilt by scanning the WAL.
+ - The WAL index is non-authoritative; if missing or corrupt, implementations MAY rebuild it by scanning the committed WAL window,
+   or MAY fall back to WAL scans for lookups.

---

 5. Set `ckpt_end_offset = wal_tail_offset` and apply WAL records in order from `wal_head_offset` through `ckpt_end_offset`.
-6. Rebuild buckets and counters from slots (clear all buckets, reinsert live slots, recompute counters).
-7. `msync` base ranges (slots + buckets).
-8. Publish checkpoint:
+6. Update the base snapshot:
+   - Implementations SHOULD apply WAL records to base slots and update base buckets/counters **incrementally** while applying:
+     - For PUT: update existing slot if key exists; otherwise append a new slot and insert into buckets.
+     - For DEL: mark slot tombstoned (clear USED) if present; update buckets appropriately (tombstone handling).
+   - Implementations MAY instead perform a full rebuild of buckets/counters from slots (clear all buckets and reinsert live slots).
+     A full rebuild is recommended when bucket tombstones/probe lengths become excessive, or when `with_gc=true` is requested.
+7. `msync` base ranges that were modified (slots + buckets).
+8. Publish checkpoint:
    - advance `wal_head_offset` to the record after `ckpt_end_offset` (if WAL empty, `wal_head_offset == wal_tail_offset`)
    - update `overlay_live_delta` to reflect only uncheckpointed WAL records
    - in ordered-keys mode, if WAL becomes empty, set `overlay_tail_key = base_tail_key`
+   - if a WAL index is present, implementations SHOULD clear it when WAL becomes empty; with `with_gc=true`, implementations SHOULD clear or rebuild it
    - set `base_generation` to a new even value
    - clear `reader_pause=0`
    - recompute and store `header_crc32c` (required if any CRC-covered header bytes changed during the checkpoint)
    - `msync` the header

---

+#### Optional checkpoint GC (`with_gc`)
+
+Implementations MAY expose a checkpoint option (e.g. `with_gc=true`) that performs additional maintenance during checkpoint, such as:
+- full rebuild of buckets/counters from slots (rehash)
+- clearing/rebuilding any optional WAL index structures
+- other compaction/maintenance tasks
+
+`with_gc` MUST NOT change correctness; it only affects performance and space usage.

---

This is already addressed by Patch 7 and Patch 4/8, but make sure you delete/adjust any remaining MUST language that implies “index rebuild is required.”

Specifically, in the original Recovery/Open section you had:

“Rebuild the WAL index by scanning …”

Patch 7 fully replaces that, so you’re covered.

---

+#### Publication ordering and memory visibility (MUST)
+
+To provide readers a consistent snapshot boundary:
+
+- Writers MUST finalize all WAL record bytes (including `crc32c`) and write the COMMIT record bytes
+  before publishing the commit to readers.
+
+- Writers MUST store `wal_tail_offset` for the commit **before** publishing `commit_seq` for the commit.
+
+- Publishing `commit_seq` MUST use an atomic store with **release** semantics.
+  Readers MUST load `commit_seq` with **acquire** semantics before reading `wal_tail_offset` or WAL bytes.
+  (Observing a new `commit_seq` implies the corresponding WAL bytes and `wal_tail_offset` are visible.)
+
+- In **WritebackSync**, the WAL durability barrier (`msync`/`fdatasync`/equivalent) MUST complete
+  after writing the COMMIT record and before publishing `commit_seq`.
+
+Note: `commit_seq`/`wal_tail_offset` are still non-durable hints on disk unless the header is explicitly synced.

---


 **StartRead:**
 1. If `reader_pause==1`, the read MUST retry or return `ErrBusy`.
 2. Read `g1 := base_generation`. If `g1` is odd, the read MUST retry or return `ErrBusy`.
-3. Read `read_seq := commit_seq` (the snapshot sequence number for this operation).
-4. Publish activity in the process’ local table and update `ReaderSlot`:
+3. Read `read_seq := commit_seq` (the snapshot sequence number for this operation).
+4. Read `wal_tail_snap := wal_tail_offset` (the committed WAL tail snapshot for this operation).
+   Implementations MUST read `wal_tail_offset` **after** reading `commit_seq`.
+5. Publish activity in the process’ local table and update `ReaderSlot`:
    - increment `active_reads`
    - recompute `read_seq_min` for this process and store it
-5. Re-check `reader_pause` and `base_generation`:
+6. Re-check `reader_pause` and `base_generation`:
    - if `reader_pause==1` or `base_generation != g1` (or now odd), undo the publication and retry/return `ErrBusy`.

---

+### Recommended WAL scan algorithms
+
+The following pseudocode illustrates correct ring-order scanning behavior. Implementations MAY use a different structure,
+but MUST preserve the same wrap/stop semantics.
+
+Define helpers:
+
+```
+WAL_HDR = sizeof(WalRecordHeader)   // 32
+
+fn wal_advance(off, n):
+  off2 = off + n
+  if off2 == wal_region_end_offset: return wal_offset
+  return off2
+
+fn wal_bytes_to_end(off):
+  return wal_region_end_offset - off
+
+fn wal_read_header(off) -> WalRecordHeader:
+  // reads 32 bytes at off
+
+fn wal_record_is_valid(off, hdr) -> bool:
+  // Must implement the Validity rules:
+  // - record_size sanity and 8-byte alignment
+  // - record fits within [off, wal_region_end_offset) (no crossing)
+  // - type is known
+  // - expected record_size matches record type definition
+  // - CRC32C matches
+```
+
+#### Tail discovery scan (find last committed COMMIT)
+
+```
+fn wal_scan_find_tail(start_off, seed_prev_txn_seq) -> (last_commit_seq, last_commit_end_off):
+  off = start_off
+  scanned = 0
+  prev_seq = seed_prev_txn_seq
+  last_commit_seq = 0
+  last_commit_end = start_off
+
+  while scanned < wal_size:
+    // implicit PAD if fewer than WAL_HDR bytes remain
+    if wal_bytes_to_end(off) < WAL_HDR:
+      scanned += wal_bytes_to_end(off)
+      off = wal_offset
+      continue
+
+    hdr = wal_read_header(off)
+
+    // basic header sanity (record_size/type range checks happen inside wal_record_is_valid)
+    if hdr.record_size < WAL_HDR or (hdr.record_size % 8) != 0:
+      break
+
+    // stop if txn_seq decreases (reclaimed old ring contents)
+    if hdr.txn_seq < prev_seq:
+      break
+
+    if !wal_record_is_valid(off, hdr):
+      break
+
+    prev_seq = hdr.txn_seq
+
+    next_off = wal_advance(off, hdr.record_size)
+    scanned += hdr.record_size
+
+    if hdr.type == PAD:
+      // PAD consumes to end-of-region; next_off must be wal_offset
+      off = wal_offset
+      continue
+
+    if hdr.type == COMMIT:
+      last_commit_seq = hdr.txn_seq
+      last_commit_end = next_off
+
+    off = next_off
+
+  return (last_commit_seq, last_commit_end)
+```
+
+#### Window scan (iterate exactly `[head, tail)` in ring order)
+
+```
+fn wal_scan_window(head, tail, visit_record):
+  off = head
+  scanned = 0
+  while off != tail and scanned < wal_size:
+    if wal_bytes_to_end(off) < WAL_HDR:
+      off = wal_offset
+      continue
+
+    hdr = wal_read_header(off)
+    // Within the committed window, records SHOULD validate; invalid here indicates corruption or a bad tail bound.
+    if !wal_record_is_valid(off, hdr):
+      return ErrCorrupt
+
+    next_off = wal_advance(off, hdr.record_size)
+    scanned += hdr.record_size
+
+    if hdr.type == PAD:
+      off = wal_offset
+      continue
+
+    visit_record(off, hdr)
+    off = next_off
+
+  return Ok
+```

---

+#### Scan start selection (TailReplay vs FullReplay)
+
+Let `head = wal_head_offset`.
+
+- In **FullReplay**, implementations MUST begin tail discovery at `start_off = head` with `seed_prev_txn_seq = 0`.
+
+- In **TailReplay**, implementations SHOULD attempt to start tail discovery after a validated marker COMMIT record:
+  1. Attempt to validate the header marker (`commit_seq`, `wal_tail_offset`) against WAL contents as described in
+     “Runtime marker fields (non-durable hints)”.
+  2. If marker validation succeeds, set:
+     - `start_off = wal_advance(commit_off_hint, COMMIT_SIZE)` (i.e. byte after COMMIT, wrapped)
+     - `seed_prev_txn_seq = commit_seq` (seed monotonicity check)
+  3. Additionally, if the record at `head` validates, let `head_seq = record_at(head).txn_seq`. If `commit_seq < head_seq`,
+     the marker MUST be rejected (it points behind the current head).
+  4. If marker validation fails, start from `start_off = head`, `seed_prev_txn_seq = 0`.

---

 #### Overlay reconstruction (MUST)
 
 After the committed tail is known, implementations MUST reconstruct overlay metadata from the committed WAL window:
 
-- If `FLAG_ORDERED_KEYS` is set, implementations MUST reconstruct `overlay_tail_key` by replaying committed WAL records in WAL order and tracking WAL-only **new inserts**
-  (PUTs that transition a key from absent→present relative to the base snapshot and prior WAL ops). If there are no committed WAL new inserts,
-  `overlay_tail_key` MUST be set to `base_tail_key`.
+Implementations MUST scan the committed WAL window `[wal_head_offset, wal_tail_offset)` in WAL order and compute:
+
+1) `overlay_tail_key` (if `FLAG_ORDERED_KEYS` is set)
+2) `overlay_live_delta` (if used)
+
+The following pseudocode illustrates a correct algorithm:
+
+```
+// Per-key state tracked during replay
+struct KeyState { base_present: bool, cur_present: bool }
+
+fn base_contains(key) -> bool:
+  // true iff key is present as LIVE in base snapshot (USED bit set)
+
+overlay_tail_key = base_tail_key
+overlay_live_delta = 0
+map = HashMap<key, KeyState>()
+
+wal_scan_window(wal_head_offset, wal_tail_offset, |off, hdr| {
+  if hdr.type != PUT and hdr.type != DEL:
+    return
+
+  key = read_key_bytes(off, hdr)
+  st = map.get(key)
+  if st == nil:
+    bp = base_contains(key)
+    st = KeyState{ base_present: bp, cur_present: bp }
+    map[key] = st
+
+  if hdr.type == PUT:
+    // Absent -> present transition relative to base and prior WAL ops
+    if st.cur_present == false:
+      overlay_live_delta += 1
+      if FLAG_ORDERED_KEYS and st.base_present == false:
+        overlay_tail_key = key   // last WAL-only new insert in WAL order wins
+    st.cur_present = true
+
+  else if hdr.type == DEL:
+    if st.cur_present == true:
+      overlay_live_delta -= 1
+    st.cur_present = false
+})
+
+if FLAG_ORDERED_KEYS and no WAL-only new insert occurred:
+  overlay_tail_key = base_tail_key
+```
+
+If `FLAG_ORDERED_KEYS` is not set, `overlay_tail_key` may be left unchanged or set to `base_tail_key` by convention.
 
 - If `overlay_live_delta` is used, implementations MUST reconstruct it from the committed WAL overlay (net effect at recovered `commit_seq` relative to the base snapshot).
 
 Note: Implementations MAY compute these during the tail-discovery scan (e.g. by buffering per-txn until COMMIT) or in a second pass over the committed window;
 the required outcomes above are what matters.

---

 #### Get (point lookup)
-1. StartRead → snapshot `read_seq`.
-2. Consult WAL index to get the latest record for the key. If present:
-   - If `txn_seq > read_seq`, follow `prev_record_offset_plus1` until `txn_seq <= read_seq` or the chain ends.
-   - If a visible record is found: return PUT or miss for DEL.
-3. If no visible WAL record exists, fall back to base buckets/slots.
-4. EndRead.
+1. StartRead → capture `read_seq` and `wal_tail_snap` for this operation.
+2. Let `head = wal_head_offset` (read after StartRead’s base_generation checks, or read once and treat changes as `ErrBusy`).
+
+3. Attempt WAL overlay lookup:
+   - If WAL index is enabled and configured for use:
+     a) Probe the WAL index for `key`. For each candidate entry:
+        - decode `off = record_offset_plus1-1`
+        - if `off` is out of WAL bounds, continue probing
+        - if `off` is not in the ring interval `[head, wal_tail_snap)` (WAL order), continue probing
+        - parse and validate the WAL record at `off` (size/type/CRC); on failure continue probing
+        - verify the record key bytes equal `key`; on mismatch continue probing
+        - candidate record found → proceed
+     b) If no candidate record found → go to step 4 (fallback scan/base).
+
+     c) Snapshot filtering:
+        - If candidate `txn_seq > read_seq`, attempt to walk `prev_record_offset_plus1`:
+          while `txn_seq > read_seq` and `prev_record_offset_plus1 != 0`:
+            prev_off = prev_record_offset_plus1-1
+            if prev_off not in `[head, wal_tail_snap)`: break
+            validate record at prev_off and key match; else break
+            candidate = prev_record
+          If after this `candidate.txn_seq > read_seq`, treat as “no visible WAL record” and proceed to base lookup (step 5).
+
+     d) Ensure latest visible version (hint index may be stale):
+        - Scan forward from the byte after `candidate` up to `wal_tail_snap` in WAL order.
+        - Stop early if a scanned record has `txn_seq > read_seq` (txn_seq is non-decreasing in WAL order).
+        - Whenever a scanned record matches `key` and has `txn_seq <= read_seq`, set `candidate = that record`.
+        - (If validation fails inside `[head, wal_tail_snap)`, return `ErrCorrupt`.)
+
+     e) If `candidate` is DEL → return NotFound. If PUT → return its value.
+
+   - If WAL index is disabled or not used → go to step 4.
+
+4. Fallback WAL scan (O(WAL)):
+   - Scan WAL records in `[head, wal_tail_snap)` in WAL order.
+   - Track the latest record for `key` with `txn_seq <= read_seq`.
+   - Stop early if the current record `txn_seq > read_seq` (monotonicity).
+   - If a matching record is found: DEL → NotFound; PUT → return value.
+
+5. Base lookup:
+   - Perform base bucket lookup; verify key bytes in slot; respect tombstones.
+
+6. EndRead.

---

+**Note:** TailReplay and FullReplay both perform the same correctness-critical steps:
+- discover committed tail (last COMMIT)
+- reconstruct `overlay_tail_key` (ordered) and `overlay_live_delta` (if used)
+
+They differ only in optional work such as rebuilding the WAL index for faster point lookups.

---

## Important: fsync failures

Concrete edit to your step 8

Something like:

8. WritebackSync durability + publish

After appending the COMMIT record, implementations MUST flush the page-aligned WAL byte ranges dirtied by this transaction (including the COMMIT record) using msync(MS_SYNC) / fdatasync / equivalent.

If this flush fails, the commit MUST fail and MUST NOT advance commit_seq.

Only after the flush succeeds may the commit advance commit_seq = txn_seq (visibility barrier for readers) and return success.

That’s the full correctness contract.

Small extra: how readers should treat commit_seq

Make sure your reader snapshot rule is:

read_seq := atomic_load_acquire(commit_seq)

Writer does atomic_store_release(commit_seq, txn_seq) after the durability barrier succeeds.

That’s the clean “publish point.”

If you want, I can suggest a short “fsync failure policy” section to paste into the spec (what to set, what to stop doing, and why header updates are best-effort only).

---

## Important, we need a reliable CROSS-process way, to "stop" all readers from accepting writes
after an fsync failure in any process(), after that all need to reopen.
