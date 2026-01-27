# Corruption, crash-safety, and durability

This document describes what kinds of corruption can occur in an `mmap`-backed slotcache file, what we detect, and what recovery actions are expected.

**Important:** slotcache can be used in two broad modes:

- **Durable / database mode** (WAL + `WritebackSync`, checkpoint uses `msync`): intended to provide *atomic, crash-consistent commits* and durability (subject to the OS/filesystem’s guarantees).
- **Fast / "throwaway" cache mode** (WAL + `WritebackNone`): commits are atomic at the logical format level but not guaranteed durable; power loss may drop recent commits.

We do **not** attempt to provide end-to-end protection against silent storage corruption (bit-rot); we rely on the filesystem/storage stack for that (ECC RAM, reliable SSDs, ZFS/btrfs checksums, etc.).

---

## 1) Threat model / sources of corruption

### 1.1 mmap faults (SIGBUS/SIGSEGV)
Certain failures surface as a **signal** when code touches the mapping:
- file truncated/overwritten externally
- underlying I/O error during page-in
- out-of-space / writeback failures on dirty mmapped pages

**Handling approach (Go):** convert faults into errors via `runtime/debug.SetPanicOnFault` and mark the mapping as faulted; subsequent operations fail fast. See `sigbus.md`.

### 1.2 Torn / partial writes after crash or power loss
Even on local filesystems, a crash can leave the file in a partially updated state.

WAL-first formats reduce the blast radius by making commits append-only and framed.

### 1.3 Software bugs / wild writes
Bugs in our code (wrong offset math, bounds errors) can write incorrect bytes into the mapping. With `mmap`, those bytes may later be persisted.

We mitigate by:
- strict bounds checks for offsets/lengths
- structural invariants
- (optionally) read-side invariant validation

### 1.4 Silent storage corruption (bit-rot)
Storage can (rarely) return incorrect bytes without reporting an I/O error.

We generally **do not checksum the entire base snapshot** (slots/pages) on every read. If the bytes are still structurally plausible, we may not detect the corruption immediately.

Operational expectation: rely on a checksumming filesystem / redundancy if this matters.

---

## 2) WAL framing: reliable detection of incomplete commits

WAL-first formats use **record framing + CRC** and a **COMMIT marker**.

On Open/Recovery:
- scan WAL records forward validating `rec_len` + CRC
- ignore invalid tail records
- treat the last valid COMMIT as the end of the last committed transaction

This provides a strong guarantee that we do not “half-apply” a transaction: a commit is either fully present (and replayable) or ignored.

This is useful even in fast/cache mode (no `msync`):
- recent commits may be lost after power loss
- but corruption in the WAL tail is detected and ignored, rather than producing undefined state

---

## 3) Checkpoint safety (base snapshot updates)

Checkpoint applies committed WAL records into the base snapshot (slots/buckets).

**Policy:** checkpoints SHOULD use `msync` barriers.

Minimal durability sequence:
1. `msync` WAL so committed records are on disk
2. apply WAL → base
3. `msync` base
4. advance `wal_head` / update header metadata
5. `msync` header

### 3.1 What can still go wrong?
`msync` is not universally failure-atomic: a crash during checkpoint can still leave the base snapshot partially updated on some storage stacks.

Recovery must therefore be designed so that:
- either the checkpoint is detectably incomplete (and we can re-run), or
- applying WAL again is idempotent / safe.

If the format does not make checkpoint replay safe, then a mid-checkpoint crash must be treated as `ErrCorrupt` and require rebuild.

---

## 4) What we validate

### 4.1 Format / header validation
- magic/version/header sizing constraints
- reserved bytes must be zero
- header CRC
- section offsets/sizes must be in-range and aligned (for aligned formats)

### 4.2 Structural invariants
Examples (format-dependent):
- counter bounds (`slot_highwater <= slot_capacity`, etc.)
- bucket table invariants
- reserved bits in slot metadata are zero

### 4.3 WAL validation
- record length bounds + alignment
- CRC32C over header+payload
- commit boundary detection

---

## 5) Recovery behavior (what callers should do)

Recommended classification:
- **Transient contention** (e.g. `ErrBusy`): retry
- **Incompatible format/config** (`ErrIncompatible`): reopen with correct options or rebuild
- **Corruption / fault** (`ErrCorrupt`, mapping faulted): rebuild (or restore from backup)

In durable/database mode, rebuild means “recover from WAL + checkpoint” (format-specific).
In cache mode, rebuild can mean “delete file and recreate from source of truth”.

---

## 6) Operational guidance

- Prefer local filesystems with good semantics (avoid network FS for mmap’d DB files)
- Use ECC RAM and reliable storage for production durability
- If you need silent corruption detection, prefer checksumming filesystems (ZFS, btrfs) or storage with end-to-end checksums
- Monitor for:
  - major page faults / reclaim pressure
  - SIGBUS-derived fault errors (indicates underlying storage issue or external modification)
