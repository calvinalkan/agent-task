# Overview

mddb is a document database for markdown files with YAML frontmatter, optimized for **agentic CLI tools** operating on local files.

The primary design choice is that **files are the API**:

- Humans and tools can `cat`, `grep`, edit, and `git`-diff the database.
- The **index is explicitly throwaway**.
- The source of truth is always the `*.mddb.md` files.

## Goals

- **Human-readable storage**: plain markdown, readable with standard tools.
- **Git-native**: replicate/history/branch/merge with git.
- **Fast indexed reads**: filter without scanning all files.
- **Typed schema**: compile-time safety (Go generics) and strict validation.
- **Crash-safe writes**: write-ahead log (WAL) for roll-forward recovery.
- **Zero server**: embedded library; no daemon.
- **External-change friendly**: simple cache invalidation protocol.

## Non-goals (v1)

- Multi-writer concurrency (single writer only).
- MVCC / snapshot isolation.
- Cross-collection transactions.
- Nested directories (flat directory only).
- Non-YAML frontmatter (TOML/JSON).
- Rollback (only roll-forward from WAL).
- Automatic file watching (watcher is opt-in).

## Architecture

```
┌─────────────────────────────────────────┐
│  Application (CLI tools, agents, etc.)  │
├─────────────────────────────────────────┤
│  mddb: schema + queries + tx + WAL      │
├─────────────────────────────────────────┤
│  slotcache: mmap index cache (SLC1)     │
├─────────────────────────────────────────┤
│  Filesystem + Git                        │
└─────────────────────────────────────────┘
```

## Key invariants

- `*.mddb.md` files are the **only authoritative state**.
- `.cache` is derived from `*.mddb.md` and may be deleted at any time **when no mddb instance is using it**.
- `Get(key)` reads the `*.mddb.md` file directly and is always fresh.
- `Filter(...)` reads the cache and is fast, but can be stale if external edits occur (see [cache management](cache.md)).

## Terminology

- **data-dir**: the directory that contains the mddb database files.
- **doc**: a `*.mddb.md` file.
- **key**: the filename stem (`<key>.mddb.md`).
- **frontmatter**: YAML block at the top of a doc.
- **index**: fixed-size encoded bytes derived from frontmatter according to the schema.
- **cache**: `<data-dir>/.cache`, stored in slotcache (SLC1) format.
