# Path mapping and on-disk layout

mddb separates **identity** (`id`) from **layout** (where the document lives on disk).

The caller controls layout via a deterministic function:

- `PathOf(id) → relativePath`

mddb appends `.mddb.md` and resolves under the data directory. The **canonical path** for an `id` is:

```
<data-dir>/<PathOf(id)>.mddb.md
```

## PathOf requirements

`PathOf` MUST:

- Be deterministic (same `id` → same path)
- Return a relative path (not absolute)
- Not escape `<data-dir>` (no `..` after normalization)
- Not return a path under `.mddb/`
- Not include the `.mddb.md` suffix (mddb appends it)

`PathOf` MUST be injective (different IDs → different paths). mddb does not enforce this at runtime — if two IDs map to the same path, writes will silently overwrite each other.

### Path normalization

Implementations SHOULD treat `PathOf` output as using `/` as separator and convert to platform separator as needed. Callers SHOULD avoid path components that differ only by case on case-insensitive filesystems.

### LayoutID

Because `PathOf` is executable code, mddb cannot detect when its semantics change.

Callers SHOULD supply a stable **LayoutID** string when opening the database, and change it when `PathOf` behavior changes. Implementations MUST incorporate `LayoutID` into cache compatibility (see [slotcache integration](010-cache.md)).

## Optional: IdFromPath

Implementations MAY accept an inverse mapping:

- `IdFromPath(relativePath) → (id, ok)`

If provided, `IdFromPath` MUST be consistent with `PathOf`:

```
PathOf(id) + ".mddb.md"  →  IdFromPath  →  same id
```

### Why this is useful

`IdFromPath` enables **skipping YAML parsing** during [rebuild](011-rebuild.md) and [watcher refresh](012-invalidation.md).

Without `IdFromPath`, to process a file you must:
1. Open the file
2. Parse YAML frontmatter
3. Extract `id`
4. Check cache freshness

With `IdFromPath`:
1. Derive `id` from path (pure string manipulation, no I/O)
2. Stat file for mtime
3. Check cache: if `id` exists with same mtime → **skip entirely**
4. Only parse YAML if cache is stale

**Example — watcher refresh:**
```
inotify: tasks/2024/01/fix-bug.mddb.md modified

→ IdFromPath("tasks/2024/01/fix-bug.mddb.md") → "fix-bug"
→ stat file → mtime unchanged from cache
→ skip (no file read, no YAML parse)
```

**On rebuild with 10,000 docs:** instead of parsing 10,000 YAML files, you stat 10,000 files and only parse the changed ones.

## Canonical membership

A file is a **canonical document** if:

1. It is at `PathOf(id).mddb.md` for some `id`
2. Its `frontmatter.id` parses and equals that `id`

Files ending in `.mddb.md` that don't satisfy this are **orphans** — not indexed, reported during rebuild.

## Get semantics

`Get(id)` MUST:

1. Compute canonical path via `PathOf(id)`
2. Read the file
3. Validate `frontmatter.id == id`

If the file exists but `frontmatter.id` doesn't match, return an error. `Get(id)` MUST NOT scan the filesystem.

## Directory creation

When writing a document, mddb MUST create missing parent directories.
