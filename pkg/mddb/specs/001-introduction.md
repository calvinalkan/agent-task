# Introduction

This document defines the mddb specification. See the [README](./README.md) for motivation and comparisons to alternatives.

## Normative language

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** in this spec are to be interpreted as described in RFC 2119.

## Design principles

1. **Files are authoritative.** The `*.mddb.md` files are the only source of truth.
2. **The cache is disposable.** The [`slotcache`](../../slotcache/specs/README.md) index is a derived optimization layer; it MUST be safe to delete and rebuild at any time.
3. **Single-writer simplicity.** mddb supports multi-reader, single-writer usage.
4. **Crash-safe commits via WAL.** mddb uses a write-ahead log (WAL) to guarantee roll-forward recovery.

## Non-goals (v1)

- Multi-writer concurrency / merging
- MVCC / snapshot isolation
- Rollback ("undo") recovery — mddb is roll-forward only
- General SQL / arbitrary query planning
- Automatically choosing ID formats or directory layouts

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                        mddb                             │
│  ┌───────────────────────────────────────────────────┐  │
│  │  Index Schema (frontmatter → fixed-size bytes)    │  │
│  └───────────────────────────────────────────────────┘  │
│              │                         │                │
│              ▼                         ▼                │
│  ┌─────────────────────┐   ┌─────────────────────────┐  │
│  │  *.mddb.md files    │   │  slotcache (throwaway)  │  │
│  │  (source of truth)  │   │  key → index bytes      │  │
│  └─────────────────────┘   └─────────────────────────┘  │
│         ▲        │                     ▲                │
│         │        │    rebuild/refresh  │                │
│         │        └─────────────────────┘                │
└─────────┼───────────────────────────────────────────────┘
          │
    external edits (if allowed/wanted)
    (agents, editors, git)
```

- **Documents:** `*.mddb.md` files with YAML frontmatter — the source of truth, editable by any tool.
- **Index schema:** caller-provided schema that compiles frontmatter fields into fixed-size bytes.
- **slotcache:** mmap-friendly key→index store, derived from documents, safe to delete.

`slotcache` is treated as a library dependency. mddb MUST NOT require `slotcache` to rebuild itself; only mddb can rebuild because only mddb knows how to enumerate and parse documents.
