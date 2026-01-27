# Cache invalidation and watcher

mddb treats the `slotcache` index as **throwaway**. External tools may edit, move, or delete documents without involving mddb.

Because the cache is derived, mddb provides a simple invalidation protocol:

- If the cache might be stale, delete/replace it and rebuild/refresh.

This document specifies:

- the mtime-based invalidation convention
- the rules for safe invalidation while mddb is running
- an optional file watcher integration

## mtime high-water mark convention

mddb uses the cache file’s mtime as a “high water mark” for its own writes.

- Cache file: `<data-dir>/.mddb/cache.slc`
- Document files: `.../*.mddb.md`

Invariant on successful commit:

- after mddb finishes updating document files, it updates the cache file
- therefore `mtime(cache.slc) >= mtime(doc)` for every document file written by that commit

### Detecting external modifications (best effort)

If an external tool modifies a document file after the last mddb commit, then typically:

- `mtime(doc) > mtime(cache.slc)`

An external tool (or a caller willing to scan) MAY use this to decide to delete the cache.

## Per-entry revision verification

`slotcache` stores a per-entry `revision` (typically `mtime_ns`) for each document.

mddb MAY expose a `Query` option (e.g., `VerifyRevisions`) that compares the canonical file’s `mtime_ns` to the cached revision and returns `ErrCacheStale` if mismatched.

This is more precise than the cache-mtime heuristic, but it costs a `stat()` per match.

## Safe invalidation

### When mddb is not running

If no mddb process has the cache mapped/open, it is safe to delete the cache file:

- delete `<data-dir>/.mddb/cache.slc`

The next `Open()` or `Query()` can rebuild it.

### When mddb is running

Callers MUST NOT delete or truncate the cache file behind a live mddb instance.

On many Unix-like systems, an `mmap` remains valid after file deletion or `rename()`-based replacement, causing the process to continue reading stale data indefinitely.

Instead, callers SHOULD use an explicit API:

- `InvalidateCache()` (implementations SHOULD use slotcache's invalidation mechanism so long-lived mmapped readers observe `ErrInvalidated` and remap)
- or `Refresh()` / `Rebuild()`

Implementations that replace `<data-dir>/.mddb/cache.slc` MUST follow the safe swap protocol described in [slotcache integration](010-cache.md#cache-invalidation-and-replacement).

## Watcher (optional)

Long-running applications (TUI/GUI/daemon-like) may want to react to external edits.

mddb MAY provide a watcher facility that:

- monitors the database directory for changes to `*.mddb.md` files
- on a detected change, invalidates the cache and/or triggers refresh/rebuild
- emits events so the application can refresh UI/state

The watcher is opt-in; mddb MUST NOT start watching automatically.

### Suggested watcher behavior

A practical default is:

1. Watch for filesystem events affecting `*.mddb.md` files (create/modify/delete/rename).
2. Debounce/coalesce events (many editors write temp files or do multiple renames).
3. Prefer calling `Refresh()` (incremental) if available; otherwise call `InvalidateCache()`.
4. Emit an event (optionally including an `id` if it can be determined cheaply).

If the caller provides `IdFromPath`, the watcher can often map an event path to an `id` without parsing YAML.

This avoids returning silently stale filter results in long-running applications.

## Known limitations

- mtime-based staleness detection is best-effort and does not reliably detect deletions or renames without a watcher.
- filesystems with coarse mtime resolution can miss same-tick edits.
- some tools can edit files while preserving mtime, defeating mtime-based detection.

Because the cache is throwaway, the recovery strategy is always the same:

- invalidate/refresh and rebuild if needed.
