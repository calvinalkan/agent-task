# fmcache: mmap-friendly snapshot cache format ("FMC1")

fmcache is a throwaway, file-backed cache optimized for fast reads and **index-only** filtering.

This document specifies:
- the public API contract (behavior + error model)
- the on-disk snapshot format v1 (magic `"FMC1"`)

fmcache is designed to be cheap to invalidate and rebuild. It is **not** a primary data store.

## Goals

- Fast reads via `mmap` (Unix).
- Cheap invalidation/rebuild (throwaway cache semantics).
- Fast filtering using a fixed-size per-entry `index` (without reading variable-length `data`).

## Non-goals (v1)

- WAL/journaling.
- Multi-process conflict resolution/merging.
- Durable database semantics (throwaway cache).
- Incremental/on-disk in-place updates (writes are snapshot rewrites).
- Built-in full-text search (callers should search the source-of-truth separately).

## Tradeoffs and Limitations

This section documents intentional constraints in v1 and typical ways to evolve.

- Snapshot commits: `Commit()` rewrites the full snapshot; write cost is `O(entry_count + total_data_bytes)`.
- Single-writer semantics: multi-process coordination is not handled; callers MAY lock externally if needed.
- Ordering: only key order is guaranteed; no on-disk secondary indexes.
- File size limit: v1 uses `uint32` for `data_offset` and `data_length`, so the file MUST be `< 4GiB`.
  - Mitigations: keep `MaxDataLen` small or `0` (index-only), or shard/partition into multiple cache files.

Potential future changes (new format versions) MAY include:

- 64-bit offsets/lengths (lifting the `< 4GiB` limit).
- Segmented snapshots or WAL-style durability for frequent commits.
- Additional section offset fields in the header (more flexible layouts).

## Normative Language

The key words **MUST**, **MUST NOT**, **SHOULD**, **SHOULD NOT**, and **MAY** are used as follows:

- **MUST / MUST NOT**: absolute requirements for a conforming implementation.
- **SHOULD / SHOULD NOT**: strong recommendations; valid reasons may exist to deviate, but the tradeoffs must be understood and accepted.
- **MAY**: optional behavior.

In this document, **implementation** refers to the fmcache library, and **caller** refers to application code using the library.

## Concepts

### Logical entry

Each cache entry consists of:

- `key`: fixed-size, NUL-padded bytes on disk (stored in the index section)
- `revision`: `int64` supplied by the caller (e.g. source mtime, generation)
  - Used by caller code to detect staleness (e.g. "is this cached entry still valid for the current source revision?").
  - Not interpreted by fmcache; it is stored and returned verbatim.
- `index`: fixed-size opaque bytes (for filtering)
- `data`: variable-length opaque bytes (optional; may be empty)

### File is a snapshot

The file on disk is a single **snapshot** with this layout:

1. Fixed-size header (64 bytes in v1).
2. Index section with fixed-size entries, sorted by key.
3. Data section with concatenated data blobs.

`Commit()` writes a whole new snapshot and atomically replaces the previous file.

## Public API Contract

The public API is split into:

- `ByteCache`: bytes-in/bytes-out, format-agnostic `(index,data)`
- `Cache[T,I]`: typed wrapper around `ByteCache`

### Low-level: `ByteCache`

The low-level API operates on bytes (`index` + `data`) and is format-agnostic.

```go
type SyncMode int

const (
    // SyncNone does not call fsync. Fastest, least durable.
    SyncNone SyncMode = iota // default

    // Sync fsyncs the temp file before rename, but does not fsync the directory.
    // After a crash, the rename may not be durable (stale snapshot may remain).
    Sync

    // SyncFull fsyncs the temp file, renames into place, then fsyncs the directory.
    // Most durable.
    SyncFull
)

type Options struct {
    // KeySize is persisted as uint16 in the header.
    // MUST be in [1, 65535]. Default 32.
    KeySize    int

    // IndexSize is persisted as uint16 in the header.
    // MUST be in [0, 65535].
    //
    // IndexSize is required for OpenByteCache and MUST be > 0.
    // For Open[T,I], IndexSize can be 0 and will be derived from I.
    // If non-zero, it MUST match the derived size.
    IndexSize  int

    // MaxDataLen limits per-entry data size.
    // Default 64*1024. Use 0 for index-only caches (no data allowed).
    // MUST be in [0, 2^32-1] (stored as uint32 in the header).
    // Stored in the header for invalidation.
    MaxDataLen int

    SchemaVersion uint16 // caller-controlled schema version, default 1
    SyncMode   SyncMode
}

type ByteCache struct {
    // thread-safe within a process
}

type ByteEntry struct {
    Key      string
    Revision int64

    // Index and Data are borrowed views into the cache's current backing store
    // (mmap'd file and/or in-memory pending buffers).
    //
    // They are only valid until the next mutation that may rebuild/replace the
    // backing store (Put/Delete/Commit/Close). Copy them if you need to retain
    // bytes.
    //
    // Callers MUST NOT call Commit/Close concurrently with code that still
    // reads these slices.
    Index []byte // len == IndexSize
    Data  []byte // len <= MaxDataLen
}

type IndexMatch struct {
    Key      string
    Revision int64

    // Index is copied into the result slice (safe to retain).
    Index []byte // len == IndexSize
}

type FilterOpts struct {
    Reverse bool // false = key asc, true = key desc
    Offset  int  // skip first N matches; MUST be >= 0
    Limit   int  // 0 = no limit; MUST be >= 0
}

func OpenByteCache(path string, opts Options) (*ByteCache, error)

// Close releases resources. It does not call Commit; uncommitted changes are discarded.
func (c *ByteCache) Close() error

func (c *ByteCache) Len() (int, error)

func (c *ByteCache) Get(key string) (ByteEntry, bool, error)
func (c *ByteCache) Put(key string, revision int64, index, data []byte) error
func (c *ByteCache) Delete(key string) (bool, error)

// FilterIndex scans index entries in key order (ascending or descending per opts).
// It MUST NOT read data blobs.
//
// It only allocates/copies for returned matches (i.e. offset/limit are applied
// during the scan).
//
// If opts.Offset or opts.Limit is negative, it returns ErrInvalidFilterOpts.
// If opts.Offset is beyond the number of matches, it returns ErrOffsetOutOfBounds (treat as a paging error).
func (c *ByteCache) FilterIndex(opts FilterOpts, match func(key string, revision int64, index []byte) bool) ([]IndexMatch, error)

// AllEntries is a convenience wrapper for FilterIndex(..., matchAll).
// It returns index entries only and MUST NOT read data blobs.
func (c *ByteCache) AllEntries(opts FilterOpts) ([]IndexMatch, error)

// Commit persists pending changes to disk via atomic replace.
func (c *ByteCache) Commit() error
```

#### `OpenByteCache` requirements

- If `path` does not exist, `OpenByteCache` MUST create it and return an empty cache (no error).
- A newly created cache file MUST be initialized with an empty snapshot header (`entry_count = 0`).
- `OpenByteCache` MUST NOT create parent directories.
- When creating a new file, the implementation SHOULD use permissions `0600`.
- The implementation MUST NOT change file permissions/mode for existing files (including during `Commit`).
- If the file exists but is empty (zero-length), the implementation SHOULD treat it as uninitialized and write an empty snapshot header (`entry_count = 0`) while preserving existing permissions.

#### Read/write model

- `Put`/`Delete` mutate an in-memory view; only `Commit` persists changes.
- `Close` MUST NOT call `Commit`; it discards uncommitted changes.
- Read-your-writes: within one `ByteCache` instance, `Get`/`FilterIndex` MUST reflect uncommitted `Put`/`Delete`.
- `Len` MUST reflect the current in-memory view (including uncommitted changes) and SHOULD be `O(1)`.

#### Borrowed bytes

- `Get` returns borrowed `Index`/`Data` slices that are only valid until the next mutation that may rebuild/replace the backing store (`Put`/`Delete`/`Commit`/`Close`). Callers MUST copy if they need to retain bytes.
- `FilterIndex` MUST return copied `Index` bytes in the result slice.
- `FilterIndex` passes a borrowed `index` slice to the predicate; the predicate MUST NOT retain it.

### High-level: typed `Cache[T,I]`

A typed wrapper maps between user values `T` and `(index,data)`.

```go
type Schema[T any, I any] struct {
    Encode func(*T) (idx I, data []byte, err error)
    Decode func(idx I, data []byte) (T, error)
}

type Match[I any] struct {
    Key      string
    Revision int64
    Index    I
}

// Cache uses an underlying ByteCache.
// idx I is encoded/decoded to/from bytes (default: encoding/binary).
// I must be fixed-size encodable (no slices/maps/strings/pointers).
func Open[T any, I any](path string, opts Options, schema Schema[T, I]) (*Cache[T, I], error)

type Entry[T any] struct {
    Key      string
    Revision int64
    Value    T
}

func (c *Cache[T, I]) Len() (int, error)

func (c *Cache[T, I]) Get(key string) (T, bool, error)
func (c *Cache[T, I]) GetEntry(key string) (Entry[T], bool, error)
func (c *Cache[T, I]) Put(key string, revision int64, value T) error

// FilterIndex scans index entries and evaluates the predicate using only the typed index.
// It MUST NOT read data blobs.
//
// If opts.Offset or opts.Limit is negative, it returns ErrInvalidFilterOpts.
// If opts.Offset is beyond the number of matches, it returns ErrOffsetOutOfBounds (treat as a paging error).
func (c *Cache[T, I]) FilterIndex(opts FilterOpts, match func(key string, revision int64, idx I) bool) ([]Match[I], error)

// AllEntries is a convenience wrapper for FilterIndex(..., matchAll).
// It returns index entries only and MUST NOT read data blobs.
func (c *Cache[T, I]) AllEntries(opts FilterOpts) ([]Match[I], error)

func (c *Cache[T, I]) Commit() error

// Close releases resources. It does not call Commit; uncommitted changes are discarded.
func (c *Cache[T, I]) Close() error
```

Typed-index requirements:

- Index types `I` SHOULD use only fixed-size fields (integers, fixed arrays). Avoid `string`, slices, maps, pointers.
- The default codec uses `encoding/binary` (little-endian) and encodes/decodes `I` via `binary.Write`/`binary.Read`.
- For `Open[T,I]`, `IndexSize` is derived from `binary.Size(I{})` and MUST be `> 0`.
- Any change to `I` (or its encoding/meaning) MUST bump `SchemaVersion`.
- If a consumer needs complete control over index encoding (bitpacking, custom endianness, etc.), they SHOULD use `ByteCache` directly.

## Error Model

### Rebuild-class errors (open-time)

Open-time validation MAY return:

- `ErrCorrupt`: file exists but failed structural validation (truncated, out-of-bounds offsets, etc.).
  - A non-empty file shorter than the 64-byte header MUST be treated as corrupt.
- `ErrIncompatible`: magic/schema/config mismatch; the caller should rebuild.
  - Examples: magic mismatch (`"FMC1"` vs other), `opts.SchemaVersion` mismatch, `KeySize`/`IndexSize`/`MaxDataLen` mismatch.

Callers SHOULD treat these as "cache needs rebuild" outcomes (delete/recreate the file, or ignore and proceed without cache).

### Operational errors (runtime)

Operational errors include:

- `ErrClosed`: operating on a closed cache
- `ErrInvalidKey`: invalid key passed to `Get`/`Put`/`Delete` (empty, contains NUL, too long, etc.)
- `ErrIndexSizeMismatch`: `Put` called with index bytes of wrong length
- `ErrDataTooLarge`: `Put` called with data longer than `MaxDataLen`
- `ErrOffsetOutOfBounds`: `FilterIndex`/`AllEntries` called with an offset beyond the number of matches
- `ErrInvalidFilterOpts`: `FilterIndex`/`AllEntries` called with negative `Offset` or `Limit`
- `ErrInvalidOptions`: invalid options (e.g. KeySize/IndexSize/MaxDataLen out of range)
- Underlying filesystem errors from open/read/write/sync/rename MAY be returned.

## On-Disk Format (v1)

All numeric fields are little-endian.

### File layout and sizes

- Header size is fixed at 64 bytes.
- In v1, section offsets are implied (not stored explicitly).

Definitions:

- `header_size = 64`
- `index_start = header_size`
- `entry_size = key_size + 8 (revision) + 4 (data_offset) + 4 (data_length) + index_size`
- `data_start = index_start + entry_count * entry_size`

`data_offset` values are absolute offsets from the start of the file.

For `data_length > 0`, `data_offset` MUST be `>= data_start` and `data_offset + data_length` MUST be within file bounds.

### Header

The header stores enough information to:

- validate the file
- invalidate on config changes (throwaway cache semantics)
- locate index/data sections

Open-time validation SHOULD be lightweight (cache semantics): structural/bounds checks and config/schema-version matching. Full validation of sortedness/uniqueness and every `(data_offset,data_length)` pair MAY be skipped for speed.

Write-time validation MUST be strict: `Commit` MUST only write a structurally valid snapshot (sorted unique keys, in-bounds offsets/lengths, config fields including schema version match `opts`). If it cannot, it MUST return an error and leave the previous file intact.

Read-time validation is best-effort: operations MAY detect corruption and return `ErrCorrupt`; they are not required to proactively scan/validate the entire file.

If an operation needs to materialize `Data` for an entry (e.g. `Get`) and finds that `(data_offset,data_length)` is out of bounds, it MUST return `ErrCorrupt`.

Header layout (64 bytes):

- `0..3`: `magic [4]byte` = `"FMC1"` (library format v1)
- `4..5`: `schema_version uint16` (caller-controlled; MUST match `opts.SchemaVersion`)
- `6..7`: `key_size uint16`
- `8..9`: `index_size uint16`
- `10..11`: reserved (MUST be zero)
- `12..15`: `max_data_len uint32` (MUST match `opts.MaxDataLen`; `0` means no data allowed)
- `16..19`: `entry_count uint32`
- `20..63`: reserved (MUST be zero)

All reserved bytes MUST be zero for `"FMC1"`. If any are non-zero, implementations SHOULD return `ErrIncompatible`.

### Index section

Index entries are fixed-size and sorted by key (lexicographic byte order on the key bytes, prior to NUL-padding). This is a format guarantee: `Commit` MUST write entries in sorted key order, and `FilterIndex` MUST return matches in that order (not insertion order).

Because keys MUST NOT contain NUL (`\x00`), comparing key bytes pre-padding is equivalent to comparing the fixed-size, NUL-padded key fields byte-by-byte.

Duplicate keys are invalid. Implementations MAY skip full sortedness/uniqueness validation at `OpenByteCache` for speed; if duplicates/out-of-order keys are detected during operations (e.g. while scanning in `FilterIndex`), they MUST return `ErrCorrupt`.

Each index entry:

- `key [key_size]byte` (NUL-padded; empty key is invalid)
- `revision int64`
- `data_offset uint32` (offset into file)
- `data_length uint32` (bytes)
- `index [index_size]byte` (opaque)

Notes:

- `data_offset` is `uint32` in v1, so the cache file MUST be `< 4GiB`. If a commit would exceed this, `Commit` MUST return an error and callers SHOULD shard/partition caches.
- `data_length` is `uint32` to allow `MaxDataLen` > 64KiB when configured (though small values are recommended).
- If `data_length == 0`, the entry has no data. Writers SHOULD set `data_offset = 0`, and readers MUST treat the data blob as empty (the offset is ignored).

### Data section

The data section is a concatenation of `data` blobs referenced by `(data_offset,data_length)`.

## Behavioral Semantics

### Keys

- Keys are provided as strings and treated as opaque bytes; no UTF-8 validation is performed.
- Key comparison is raw byte comparison (case-sensitive; no Unicode normalization).
- Empty keys are invalid.
- Keys MUST NOT contain NUL (`\x00`) bytes (keys are NUL-padded on disk).
- On disk, keys are stored as NUL-padded bytes in the fixed-size key field.
- When decoding a key from disk, readers SHOULD interpret the key as the bytes up to (but not including) the first NUL byte; remaining bytes are padding.
- Keys longer than `KeySize` MUST be rejected by `Put`/`Get`/`Delete`.

### Mutations

- `Put` is an upsert: it creates a new entry or overwrites an existing entry for the same key.
- `Put` MUST treat `data == nil` the same as empty data (`len(data) == 0`).
- If `MaxDataLen == 0`, all entries MUST have empty data.
- `Delete` removes an entry if present and returns whether it existed in the current in-memory view.

### Filtering and ordering

- `FilterIndex` MUST scan the index section and evaluate the predicate using only `(key, revision, index)`.
- `FilterIndex` MUST NOT read or parse the data section.
- Matches are returned in **key order** (lexicographic byte order on the key bytes, prior to NUL-padding).
- `FilterOpts.Reverse` requests key-desc ordering.
- `Offset`/`Limit` MUST be applied during the scan (do not allocate for skipped results).

Key order is the only ordering the format guarantees. Sorting by non-key fields is intentionally not built into v1; callers can collect matches and sort by decoded index fields if needed (still index-only).

If you want key order to correlate with time ("oldest/newest"), choose keys that are lexicographically time-ordered, e.g. fixed-width timestamp prefixes (`YYYYMMDDHHMMSS_...`) or time-ordered IDs (e.g. UUIDv7).

### Persistence and durability

- `Commit` MUST write a full new snapshot (header + full index + full data) to a temp file and atomically rename it over the original.
- The temp file MUST be created in the same directory as the target path so that `rename` is atomic.
- This prevents partially-written/broken cache files in normal operation (no concurrent readers observing a partial write).

Crash-durability is configurable (SQLite-style) via `Options.SyncMode`:

- `SyncNone` (default): no fsync. After a crash, the latest commit MAY be lost, the file MAY reopen as `ErrCorrupt` (rebuild), or the previous (stale) snapshot MAY still be present.
- `Sync`: fsync temp file before rename. After a crash, the new snapshot's contents are less likely to be truncated/empty, but the rename MAY not be durable (stale snapshot may remain).
- `SyncFull`: fsync temp file, rename into place, then fsync the containing directory; most durable.

If you want to amortize directory fsync cost across multiple commits in the same directory, callers MAY use `Sync` for each commit and fsync the directory once at a higher-level transaction boundary.

Only `SyncFull` aims to make a successful `Commit()` survive power loss; `SyncNone` and `Sync` MAY reopen with a stale snapshot or require rebuild.

Complexity: `Commit` is **O(number of entries + total data bytes)** (full snapshot rewrite). The design optimizes for many reads and occasional writes.

### Concurrency

- A single `ByteCache` instance MUST be safe for concurrent calls within a process.
- `Commit()` and `Close()` are synchronous and SHOULD be treated as exclusive operations; callers SHOULD NOT run them concurrently with other methods or with code still using borrowed slices returned by `Get()` or passed to the `FilterIndex` predicate.
- `Close()` SHOULD make a best-effort attempt to release resources even if it returns an error.
- Multi-process coordination (single-writer enforcement) is intentionally not handled in v1; callers MAY lock externally if needed.
