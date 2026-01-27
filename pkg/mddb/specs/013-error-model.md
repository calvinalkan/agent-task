# Errors and diagnostics

This document describes the error model for mddb.

## Principles

- Errors SHOULD be classifiable via sentinel error codes (e.g., `errors.Is`).
- Errors SHOULD carry structured details for diagnostics.
- Rebuild SHOULD produce a report that helps users fix documents.

## Sentinel error codes (suggested)

Implementations SHOULD expose (at least) the following error classifications:

### Document/ID errors

- `ErrInvalidID`: `id` fails validation (wrong characters, too long, etc.)
- `ErrReservedField`: caller attempted to set reserved frontmatter key `id`
- `ErrFrontmatterParse`: YAML/frontmatter cannot be parsed
- `ErrDuplicateID`: multiple documents declare the same `id`

### Filesystem/layout errors

- `ErrOrphanDoc`: a `.mddb.md` file is not at its canonical path (usually surfaced in rebuild report, not thrown during rebuild)
- `ErrPathEscape`: `PathOf(id)` resolved outside the data directory
- `ErrNotRegularFile`: the canonical path exists but is not a regular file (symlink, socket, device, ...)

### Transaction/WAL errors

- `ErrBusy` / `ErrLockTimeout`: cannot acquire the WAL lock
- `ErrWALCorrupt`: WAL exists but is invalid (checksum/parse failure)
- `ErrWALReplay`: WAL replay failed
- `ErrTxClosed`: operation attempted on a closed transaction
- `ErrConflict`: optimistic concurrency precondition failed (e.g., expected revision mismatch)
- `ErrDurability`: required `fsync` / durability step failed

### Cache errors

- `ErrCacheIncompatible`: cache is incompatible with current schema/ID/layout configuration
- `ErrCacheCorrupt`: cache is corrupt
- `ErrCacheInvalidated`: cache was invalidated/replaced by another process; caller should reopen/remap and retry
- `ErrCacheStale`: cache appears stale with respect to the filesystem (e.g., per-entry revision mismatch)
- `ErrNeedsRebuild`: cache is missing/invalid and an operation requires rebuild

The exact set of sentinel errors is implementation-defined; the above list is a recommended minimum.

## `Get(id)` errors

`Get(id)` returns three states:

- found: returns the parsed document
- not found: returns `(nil, false, nil)` (or equivalent)
- error: returns an error

If the canonical file exists but:

- its `frontmatter.id` is missing,
- its `frontmatter.id` does not match the requested `id`, or
- the path exists but is not a regular file,

then `Get` MUST return an error (not “not found”).

## `Query()` errors

`Query()` can fail for several reasons:

- cache missing/incompatible/corrupt/invalidated (`ErrNeedsRebuild`, `ErrCacheIncompatible`, `ErrCacheCorrupt`, `ErrCacheInvalidated`)
- writer in progress (`ErrBusy`)
- optional freshness verification detects mismatch (`ErrCacheStale`)

If `VerifyRevisions` (or equivalent) is enabled, `Query()` MUST return an error on detected staleness rather than returning silently stale results.

## Rebuild report

Rebuild SHOULD return a structured report object (even on failure).

Suggested fields:

- `indexed_count`
- `orphan_files[]`
- `parse_errors[]` (file path + error)
- `schema_errors[]` (file path + error)
- `duplicate_ids[]` (id + file paths)

Strict rebuild MUST fail (and MUST NOT publish a new cache) if any of these collections are non-empty:

- `parse_errors`
- `schema_errors`
- `duplicate_ids`

Orphans are non-fatal but SHOULD be reported.

## Error wrapping

Implementations MAY wrap underlying filesystem errors.

Callers SHOULD classify errors via sentinel checks (e.g., `errors.Is(err, ErrInvalidID)`) and not by string matching.
