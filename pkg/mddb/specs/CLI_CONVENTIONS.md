# CLI conventions and recommendations

This document is **non-normative**. It describes conventions that higher-level CLIs may adopt on top of mddb.

mddb core deliberately does not mandate:

- an ID format
- a directory layout
- “short ID” UI conventions

However, choosing a good convention can dramatically improve ergonomics and performance.

## Properties many CLIs want

A common “dream” set of properties is:

- IDs can be generated on different machines/branches without conflict
- time-based range scans are natural
- short, human-readable handles work with partial matching (“type more chars to disambiguate”)
- O(1) path derivation from ID (no database lookup to find a document)
- grep-friendly on-disk structure
- fast rebuild/refresh after external edits

All of these can be achieved purely by choosing an ID scheme and a `PathOf(id)` mapping.

## Strong default: UUIDv7 text IDs

UUIDv7 is time-ordered (lexicographically sortable when rendered in canonical text form) and globally unique.

Suggested convention:

- `id` is a canonical UUIDv7 text string
- `PathOf(id)` partitions by a prefix (e.g., first 2 bytes) or by derived timestamp components

Example `PathOf` strategies:

Note: mddb appends `.mddb.md` automatically.

- Prefix sharding:

  - `docs/<id[0:2]>/<id>`

- Time sharding (if you decode the UUIDv7 timestamp):

  - `docs/YYYY/MM/DD/<id>`

## Alternative: time+random “readable” IDs

Another ergonomic convention is a readable timestamp + randomness:

- `YYYYMMDD-HHMMSS-<base32-random>`

Example:

- `20260114-153045-7G2K9F2H0M`

This yields:

- lexicographic order == chronological order
- easy directory slicing by date

Example `PathOf`:

- `docs/YYYY/MM/DD/HHMMSS-<rand>--<slug>`

where the slug is for humans only.

## “Short IDs” and partial matching

A CLI can treat the ID as:

- a canonical key, and
- a display handle that is a prefix of the key’s entropy.

Resolution rule:

- if a prefix matches exactly one document, accept it
- if it matches multiple, prompt for more characters

Efficient prefix resolution can be implemented with:

- a cache scan (acceptable for small DBs), or
- cache prefix/range iteration primitives if available in `slotcache`.

## Ordering and pagination

mddb `Query()` order is cache slot order.

If your CLI expects chronological pagination, you have options:

- pick time-sortable IDs (UUIDv7, ULID, timestamp+random)
- enable ordered-keys mode so that scan order matches lex key order
- or sort results in-memory after filtering

### ID strategy effects (when scan order is lexicographic)

If ordered-keys mode is enabled (or you are operating on a freshly rebuilt cache with no incremental inserts), slot order matches lexicographic key order. In that case, ID strategies behave as follows:

| Key Strategy | Example | Behavior |
|--------------|---------|----------|
| ULID / KSUID | `01ARZ3NDEKTSV4RRFFQ69G5FAV` | Chronological ✓ |
| Timestamp prefix | `2024-01-15-fix-bug` | Chronological ✓ |
| Zero-padded sequential | `0001`, `0002`, `0010` | Creation order ✓ |
| Slugs | `fix-login-bug` | Alphabetical (not temporal) |
| UUID v4 | `550e8400-e29b-41d4-...` | Arbitrary (but stable) |
| Unpadded numbers | `1`, `2`, `10` | Wrong order: `1`, `10`, `2` ✗ |

If ordered-keys mode is off and incremental writes occur, scan order is insertion order; use ordered-keys or sort results in-memory for chronological ordering.

## Ordered-keys cache mode

If your ids are time-sortable (UUIDv7/ULID), enabling `slotcache` ordered-keys mode can make pagination and range queries nicer. Because ordered-keys mode can reject out-of-order inserts, implementations should either:

- surface `ErrOutOfOrderInsert` to the caller and recommend retrying with a different (larger) id, or
- keep ordered-keys mode off.

Implementations MAY rebuild the cache as a remediation, but rebuild is not required for correctness.

## Provide an inverse mapping for speed

If your layout includes the `id` in the path (for example, as the file name), you SHOULD implement `IdFromPath(path)`.

With `IdFromPath`, mddb can:

- refresh the cache after external edits without parsing YAML for unchanged docs
- map filesystem watcher events directly to ids

## Durability defaults

For interactive CLIs, `SyncData` is often a good default:

- WAL + documents are fsynced, but directories are not fsynced on every commit

For maximum safety (e.g., automation that cannot lose commits), use `SyncAll`.
