# Index schema and encoding

mddb supports an **index schema** that selects a subset of frontmatter fields and encodes them into a fixed-size byte array. These bytes are stored per-document in `slotcache` as the entry `index` payload.

## Indexable types

Indexed fields must be a subset of the [YAML subset](003-document-format.md#yaml-subset):

- **Scalars**: strings, integers, booleans ✓
- **Flat string lists** ✓
- **Flat objects** ✗ (not indexable — freeform only)

## Why a fixed-size index

- `slotcache` stores opaque fixed-size bytes per key.
- Fixed-size records enable fast mmap scanning and filtering.

The index schema is compiled at startup and defines:

- `IndexSize` (bytes for user-defined index fields; excludes mddb-reserved bytes)

mddb MUST configure `slotcache` with:

- `IndexSize == schema.IndexSize + 1`

(The extra byte is the reserved `in_wal` flag. See [Reserved: in_wal flag](#reserved-in_wal-flag).)

## Core rules

### Determinism

Given the same document frontmatter and the same schema, the encoded index bytes MUST be identical.

### Type validation

A conforming implementation MUST validate indexed fields as follows:

- **Integers:** only YAML integers are accepted (not floats, not strings). Values MUST fit the target bit width.
- **Booleans:** only YAML booleans are accepted.
- **Strings:** only YAML strings are accepted.
- **Lists:** only YAML sequences of strings are accepted.
- **Timestamps:** implementations MUST accept RFC3339/ISO-8601 strings.

If a field's YAML value does not match the expected kind, indexing fails.

### Required vs optional

By default, all indexed fields are required.

A field may be marked optional with a default value.

- If a required field is missing, indexing fails.
- If an optional field is missing, the default is used.

### Unknown fields

Fields not in the index schema are freeform and are not validated beyond the YAML subset rules.

## Field encodings

Unless otherwise stated:

- Numeric encodings are little-endian.
- Strings are UTF-8 and length-limited in **bytes**.

### Bool

- YAML: boolean (`true`/`false`)
- Encoding: 1 byte (`0` or `1`)

### Signed/unsigned integers

- YAML: integer
- Encoding: fixed-width 1/2/4/8 bytes

Values MUST be range-checked to the target width. Out-of-range values are errors.

### Enum

- YAML: string
- Schema defines an ordered list of allowed values
- Encoding: unsigned integer index into that list (commonly `uint8`)

If the YAML value is not in the allowed list, indexing fails.

### Timestamp

- YAML: string in RFC 3339 / ISO 8601 form
- Encoding: `int64` Unix nanoseconds (little-endian)

### Fixed string

- YAML: string
- Schema defines `maxLenBytes`
- Encoding: `[maxLenBytes]byte` containing UTF-8 bytes followed by NUL padding

If the string exceeds `maxLenBytes` in UTF-8 bytes, indexing fails.

### Bitset (up to 64)

- YAML: list of strings
- Schema defines an ordered list of allowed values (max 64)
- Encoding: `uint64` where bit `i` corresponds to value `values[i]`

Unknown values cause indexing failure.

Duplicate list items are allowed and treated as a no-op (setting the same bit twice).

### Fixed string list

- YAML: list of strings
- Schema defines:
  - `count` (max number of list items)
  - `itemLenBytes` (max bytes per item)
- Encoding: `count * itemLenBytes` bytes (each item is NUL-padded)

If the list has more than `count` items, indexing fails.
If any item exceeds `itemLenBytes`, indexing fails.

## Reserved: in_wal flag

mddb reserves **1 byte** at the end of the index for the `in_wal` flag. This byte is NOT part of the user-defined schema.

**Layout:**

```
[user-defined index fields...][in_wal: 1 byte]
```

**Total IndexSize** = schema-defined size + 1 byte for `in_wal`.

**Values:**

- `0` = document is committed (normal state)
- `1` = document may have pending changes in the WAL

**Behavior:**

- During commit: set to `1` before the WAL commit point and before writing documents, cleared to `0` after
- During recovery replay: MUST be set to `1` for all WAL-affected IDs *before* applying WAL records to document files; cleared to `0` after replay completes
- On read: if `in_wal = 1` is encountered, trigger recovery and retry before returning data. For `Query`, this check MUST happen while scanning (before predicate evaluation), not only on returned matches.

See [Transactions](008-transactions.md#get-and-query-behavior) for the full protocol.

## Schema evolution and rebuild

The cache is derived from documents and MUST be rebuilt when the schema changes.

Implementations MUST compute a stable schema hash and include it in cache compatibility checks (see [slotcache integration](010-cache.md)). If the cache is opened with a different schema hash, it MUST be treated as incompatible and rebuilt.

## Schema evolution guidance (non-normative)

See [SCHEMA_EVOLUTION.md](./SCHEMA_EVOLUTION.md) for examples of safe vs breaking schema changes.
