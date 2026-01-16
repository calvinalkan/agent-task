# slotcache v1: mmap-friendly slot cache format ("SLC1")

slotcache is a **throwaway, file-backed cache** optimized for:

- **Fast reads** via `mmap` (Unix)
- **Fast O(n) filtering** by scanning a dense slot array sequentially
- **Fast point lookups and updates** via a persisted `key → slot_id` hash index
- **Cheap invalidation/reset** (cache semantics; not a primary data store)

---

## Normative language

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are used as follows:

- **MUST / MUST NOT**: absolute requirements for a conforming implementation
- **SHOULD / SHOULD NOT**: strong recommendations; valid reasons may exist to deviate
- **MAY**: optional behavior

In this document:

- **implementation** refers to the slotcache library
- **caller** refers to application code using the library

---

## Goals

- **Scan-fast filtering:** scanning slots sequentially should be close to "memory bandwidth limited"
- **Fast point ops:** expected O(1) *amortized* under a good hash distribution and reasonable load factor for `Get`, `Put`, `Delete`; worst-case O(n) under heavy clustering/collisions
- **Opaque index bytes:** slotcache does not understand schemas; it stores fixed-size bytes per slot
- **Sound reads (no false positives):** read APIs MUST NOT return an entry unless the returned key bytes match the requested key under a stable even generation
- **Fail-fast on detected corruption:** if a read detects a structural invariant violation under a stable even generation, it MUST return `ErrCorrupt` (with details when possible)
- **Simple invalidation:** detect config/schema mismatch via persisted header fields; return `ErrIncompatible`

## Non-goals (v1)

- Durable database semantics (cache is throwaway)
- Self-healing or automatic rebuild from source-of-truth data (the library does not know your data universe)
- Multi-writer concurrency / merging across processes
- Secondary indexes (other than the `key → slot` primary hash index)
- Variable-length "data blob" section (v1 is index-only; callers fetch source-of-truth data)

---

## Local filesystem assumptions

slotcache targets **local filesystems** (e.g., APFS on macOS, ext4/xfs on Linux) with POSIX-like semantics:

- `mmap` with `MAP_SHARED` provides a coherent view of a regular file on a single machine
- `rename(old, new)` within the same directory is atomic
- Advisory file locks (e.g., `flock`) behave consistently for coordinating a single writer

Implementations MUST use `MAP_SHARED` (not `MAP_PRIVATE`) so that writer updates are visible to readers and the seqlock works correctly across processes.

slotcache is **not designed or tested** for network/distributed filesystems or sync layers (e.g., NFS/SMB, FUSE-based mounts, cloud-sync folders). On such systems, one or more of the above assumptions may not hold (locking semantics, rename atomicity, mmap consistency, delayed visibility), so behavior is undefined.

### External truncation is out of scope

If another process truncates or overwrites the cache file while it is mapped, the OS may raise SIGBUS or otherwise terminate the process. slotcache does not attempt to defend against this; callers MUST treat the cache file as library-owned and MUST NOT truncate it while in use.

---

## Platform notes

### macOS

- Page size may be 16KiB (Apple Silicon); this affects dirty-page granularity
- `msync` requires page-aligned address/length; implementations MUST align ranges
- `mmap` past EOF can SIGBUS; implementations MUST `ftruncate` to final size before mapping
- `fsync` may not guarantee power-loss durability; implementations that aim for maximum durability SHOULD consider `fcntl(F_FULLFSYNC)` in addition to `msync(MS_SYNC)`

### Linux

- Page size is typically 4KiB
- `msync(MS_SYNC)` provides reasonable durability guarantees on local filesystems
