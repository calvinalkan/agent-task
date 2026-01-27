# Rebuild

A rebuild constructs a new `slotcache` index by scanning and parsing the source-of-truth `*.mddb.md` files.

Rebuild is used when:

- the cache is missing
- the cache is incompatible with the current schema/ID/layout configuration
- the cache is corrupt
- the cache is full or needs compaction (tombstones)
- the caller explicitly requests rebuild

## Strictness and rebuild modes

By default, rebuild is **strict** with respect to canonical documents:

- YAML/frontmatter parse errors are errors
- `id` validation errors are errors
- indexed-field validation errors are errors
- duplicate IDs are errors

If any of the above occur, strict rebuild MUST fail and MUST NOT publish a new cache.

Implementations MAY offer a **best-effort** rebuild mode for interactive use:

- parse/id/schema errors in canonical documents are reported and those documents are omitted from the cache
- duplicate IDs remain fatal (ambiguous identity)

This mode makes it possible to keep `Query()` usable while some documents are mid-edit.

Rebuild MAY still produce a structured report describing both fatal and non-fatal issues (see [Errors and diagnostics](013-error-model.md)).

## Algorithm

A conforming rebuild implementation MUST:

1. Walk `<data-dir>/` recursively.
2. Consider each **regular file** whose name ends with `.mddb.md`, excluding the internal `.mddb/` directory.
3. Determine the candidate `id`:
   - if `IdFromPath` is provided and returns `(id, true)` for the relative path, use that `id` as a candidate fast-path
   - otherwise, parse YAML frontmatter and extract `id`
4. Validate `id` using the configured IDSpec.
5. Determine whether the file is a canonical document:
   - compute the canonical path from `id` using `PathOf(id)`
   - if the file path differs, classify it as an orphan
6. For eligible canonical documents:
   - validate and encode indexed fields using the configured index schema
7. Detect duplicate IDs.
8. Choose a cache capacity with headroom (see [slotcache integration](010-cache.md)).
9. Build a new cache file in a temporary location:
   - insert eligible canonical documents sorted by lexicographic encoded key order
10. Atomically replace the old cache with the new cache (see [Atomic publish](#atomic-publish)).

### Symlinks during enumeration

Rebuild walkers MUST NOT follow directory symlinks.

If a walker encounters a symlink whose name ends with `.mddb.md`, it MUST NOT index it and SHOULD report it as a non-fatal issue (or a fatal issue in strict security-focused configurations).

## Orphans

An “orphan” is a file that ends with `.mddb.md` but is not located at the canonical path for its declared `id`.

Orphans:

- MUST NOT be indexed into the cache
- SHOULD be included in the rebuild report

Orphans do not, by themselves, cause rebuild failure.

## Duplicate IDs

If multiple documents declare the same `id`, rebuild MUST fail.

A duplicate is detected regardless of whether one or more of the files are orphans.

## Temporary files

Rebuild implementations SHOULD ignore and/or clean up temporary files left behind by interrupted atomic writes (for example, files with suffixes like `.tmp` or `.partial`) as long as they do not end with `.mddb.md`.

## Atomic publish

Rebuild MUST publish the new cache atomically.

A conforming implementation SHOULD:

- create a new cache file at `.mddb/cache.slc.tmp` (or similar)
- fully build and close it
- if the current process has an open cache handle to the existing cache file, call `cache.Invalidate()` to force long-lived mmapped readers to remap (see [slotcache integration](010-cache.md#cache-invalidation-and-replacement))
- `rename` it over `.mddb/cache.slc`
- `fsync` `.mddb/` if the configured SyncMode requires directory durability

## Refresh (optional)

A “refresh” is an incremental maintenance operation intended for reacting to external edits without paying the full cost of a strict rebuild every time.

If implemented, `Refresh()`:

- MUST acquire the exclusive WAL lock (`flock` on `.mddb/wal`)
- MUST update the existing cache (or fall back to rebuild)
- SHOULD use file mtimes and cached per-entry `revision` to skip unchanged documents
- SHOULD use `IdFromPath` (if available) to avoid YAML parsing for unchanged docs

If refresh encounters corruption, incompatibility, or capacity exhaustion, it SHOULD fall back to a full rebuild.

## Rebuild report (non-normative)

See [Errors and diagnostics](013-error-model.md) for a suggested report shape and strict rebuild failure criteria.
