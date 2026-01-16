# slotcache Specification

This directory contains the specification for "slotcache", a high-performance, mmap-based throwaway cache.

## Documents

- **[001-overview.md](./001-overview.md)** — Scope, goals, non-goals, normative language, filesystem assumptions, platform notes

- **[002-format.md](./002-format.md)** — On-disk binary format: header layout, slots section, buckets section (hash index), CRC rules, value constraints

- **[003-semantics.md](./003-semantics.md)** — Behavioral specification: concurrency model (seqlock, locking), read/write operations, ordered-keys mode, validation rules, error taxonomy

- **[TECHNICAL_DECISIONS.md](./TECHNICAL_DECISIONS.md)** — Non-normative, repo-specific Go implementation decisions (only where the spec is implementation-defined)

## Quick reference

**File layout:**
```
┌─────────────────────────────┐
│ Header (256 bytes)          │
├─────────────────────────────┤
│ Slots (slot_capacity × N)   │
├─────────────────────────────┤
│ Buckets (bucket_count × 16) │
└─────────────────────────────┘
```

**Concurrency:** Multi-reader, single-writer via seqlock (generation counter) + optional flock.

**Key properties:**
- Fixed-size keys and index bytes
- Append-only slot allocation (tombstones, no reuse)
- Open-addressed hash index with linear probing
- Optional ordered-keys mode for range queries
