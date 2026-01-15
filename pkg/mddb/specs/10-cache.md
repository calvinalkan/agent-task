# Cache management and invalidation

mddb uses a throwaway binary cache at:

```
<data-dir>/.cache
```

The cache is stored in **slotcache v1 (SLC1)** format.

## What the cache contains

- For each doc key, the cache stores:
  - key (fixed-size bytes)
  - revision (typically file mtime as `int64` unix nanos)
  - index bytes (fixed-size, derived from schema)

The cache does not store full document content.

## Rebuild

`Rebuild()`:

- acquires the exclusive writer lock
- scans all `*.mddb.md` files
- parses frontmatter
- validates against schema
- builds a new `.cache` atomically (temp file + rename)

If any doc fails validation, `Rebuild()` fails with `ErrFieldValue` and MUST leave the existing cache unchanged.

## InvalidateCache

`InvalidateCache()`:

- acquires the exclusive writer lock
- closes the mmapped cache
- deletes `.cache`

Next `Filter()` call (or next `Open()`) will rebuild.

### Important

Do not delete `.cache` while an mddb process has it open.

On Unix, deleting an mmapped file does not invalidate existing mappings; the process may continue reading stale bytes.

Use `InvalidateCache()` instead.

## Automatic recovery

mddb treats the cache as derived state.

On `Open()` or on-demand:

- cache missing → rebuild
- cache corrupt → rebuild
- cache incompatible with schema → rebuild

If rebuild fails due to document validation errors, that error is surfaced.

## External change invalidation protocol

mddb supports a simple external invalidation protocol based on `.cache` mtime:

Invariant after an mddb commit:

- `.cache` mtime >= mtime of all `*.mddb.md` files written by that commit

External tools MAY use:

- if any `*.mddb.md` mtime > `.cache` mtime: cache is stale → delete `.cache` (only when mddb is not running)

This lets external editors/agents cheaply invalidate the cache without complex sync logic.

### Note on mmap and mtime

When `.cache` is updated via `mmap`, the filesystem mtime update may be delayed until writeback.

To make the mtime-based protocol reliable, mddb SHOULD explicitly update the `.cache` mtime (e.g., via `Chtimes`) after a successful cache update.

### Known limitation

On filesystems with coarse mtime resolution (e.g. 1s), an external edit within the same time quantum as `.cache` update may not be detected.

This is recoverable by manual `Rebuild()`.
