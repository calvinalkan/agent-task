# mddb

mddb is an embedded "markdown database" — plain markdown files with YAML frontmatter, plus a throwaway binary index ([`slotcache`](../../slotcache/specs/README.md)) for fast queries.

## What it's for

**A CRUD layer for structured markdown documents.** Wrap mddb in a CLI or library to give agents (and humans) safe, typed access to a collection of documents:

- Atomic writes with crash recovery (roll-forward WAL)
- Multi-document transactions
- Fast filtered reads on indexed frontmatter fields
- Single-writer with lock-free concurrent reads

## Why mddb?

### vs plain JSON file

A single JSON file with all records:

- **Git conflicts**: every concurrent change conflicts on the same file
- **External edits**: must diff and resync the entire dataset
- **Queries**: scan everything, no index
- **Human readable**: technically yes, but one giant blob

### vs SQLite

SQLite as the primary store:

- **Git conflicts**: binary blob, can't meaningfully version control at all
- **External edits**: hard to detect and recover from file-level changes
- **Queries**: excellent (full SQL)
- **Human readable**: no, need queries to inspect data

### vs JSON + SQLite index

JSON files with a SQLite index for queries:

- **Git conflicts**: per-file (good), but index is separate binary blob
- **External edits**: index drifts from files, need complex reconciliation to detect and repair
- **Queries**: fast (SQL on index)
- **Human readable**: JSON files yes, index no

### vs markdown + SQLite index

Markdown files with a SQLite index:

- **Git conflicts**: per-file, human-readable merge (good)
- **External edits**: same drift problem — index becomes authoritative, reconciliation is complex
- **Queries**: fast (SQL on index)
- **Human readable**: yes

### mddb's approach

Markdown files with a **throwaway** memory mapped binary cache:

- **Git conflicts**: per-file, human-readable merge (good)
- **External edits**: cache is derived, not authoritative — staleness is detected per-document (mtime), invalidation is O(1), simple inotify watcher works
- **Queries**: fast on indexed frontmatter fields (not full SQL)
- **Human readable**: yes

The key difference from "markdown + SQLite index" is that mddb's cache is **designed to be thrown away**. Index drift isn't a bug to reconcile — it's expected, and recovery is trivial (per-document mtime check, or full rebuild which is fast).

## Tradeoffs

- **Single writer** — writers serialize behind a lock; no concurrent writes
- **Roll-forward only** — WAL recovery, no rollback/undo
- **Limited queries** — filter on indexed fields, not full SQL

---

## Specification

- **[001-introduction.md](./001-introduction.md)** — Design principles, goals, architecture
- **[002-filesystem.md](./002-filesystem.md)** — Filesystem layout
- **[003-document-format.md](./003-document-format.md)** — Document format
- **[004-id.md](./004-id.md)** — IDs and key encoding
- **[005-path-mapping.md](./005-path-mapping.md)** — Path mapping and on-disk layout
- **[006-index-schema.md](./006-index-schema.md)** — Index schema and encoding
- **[007-lifecycle.md](./007-lifecycle.md)** — Concurrency model, open, close, crash recovery
- **[008-transactions.md](./008-transactions.md)** — Transactions and WAL
- **[009-query.md](./009-query.md)** — Query API
- **[010-cache.md](./010-cache.md)** — slotcache integration
- **[011-rebuild.md](./011-rebuild.md)** — Rebuild
- **[012-invalidation.md](./012-invalidation.md)** — Cache invalidation and watcher
- **[013-error-model.md](./013-error-model.md)** — Errors and diagnostics

## Non-normative companion docs

- **[API_NOTES.md](./API_NOTES.md)** — API sketches, matcher DSL notes, and examples
- **[SCHEMA_EVOLUTION.md](./SCHEMA_EVOLUTION.md)** — Schema evolution guidance and examples
- **[DATA_CACHE.md](./DATA_CACHE.md)** — Optional data-caching notes
- **[CLI_CONVENTIONS.md](./CLI_CONVENTIONS.md)** — CLI conventions and recommendations
