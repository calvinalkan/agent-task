# Schema and index encoding

mddb uses a **typed schema** to compile YAML frontmatter into fixed-size **index bytes**.

Those bytes are stored per document in the `.cache` slotcache file and are used for fast `Filter(...)` queries.

## Schema definition styles

mddb supports two schema-definition styles. They compile to the same internal schema.

### Style A: field-based

```go
var (
    Status   = mddb.Enum("status", "open", "in_progress", "closed")
    Priority = mddb.Uint8("priority")
    Blocked  = mddb.Bool("blocked").Default(false)
)

var schema = mddb.Index(Status, Priority, Blocked)
```

### Style B: struct-based

```go
type TicketStatus uint8

const (
    StatusOpen       TicketStatus = 0
    StatusInProgress TicketStatus = 1
    StatusClosed     TicketStatus = 2
)

type TicketIndex struct {
    Status   TicketStatus `mddb:"status,enum=open|in_progress|closed"`
    Priority uint8        `mddb:"priority"`
    Blocked  bool         `mddb:"blocked,default=false"`
}

var schema = mddb.IndexFor[TicketIndex]()
```

## Field types

Each field has:

- a YAML name (string)
- a binary encoding (fixed size)
- required/optional behavior (defaults)

Supported field types (v1):

| Field helper | YAML value | Binary encoding | Size |
|---|---|---:|---:|
| `Enum(name, values...)` | string | `uint8` enum index | 1 |
| `Bool(name)` | bool | `uint8` (0 or 1) | 1 |
| `Int8(name)` | int | two's complement | 1 |
| `Uint8(name)` | int | unsigned | 1 |
| `Int16(name)` | int | little-endian | 2 |
| `Uint16(name)` | int | little-endian | 2 |
| `Int32(name)` | int | little-endian | 4 |
| `Uint32(name)` | int | little-endian | 4 |
| `Int64(name)` | int | little-endian | 8 |
| `Uint64(name)` | int | little-endian | 8 |
| `Timestamp(name)` | string (ISO 8601) | `int64` unix nanos, little-endian | 8 |
| `String(name, maxLen)` | string | `maxLen` bytes, NUL-padded | `maxLen` |
| `Bitset(name, values...)` | list of strings | `uint64` bitmask, little-endian | 8 |
| `StringList(name, count, len)` | list of strings | `count * len` bytes (fixed slots), NUL-padded | `count*len` |

### Index byte layout

- The index byte array is a simple concatenation of field encodings in schema order.
- All multi-byte integers are encoded little-endian.
- Strings are UTF-8 bytes, NUL-padded to their fixed size.
- Encoding MUST reject values that do not fit the field (no truncation).

The total `IndexSize` is the sum of field sizes.

## Required vs optional fields

- All fields are **required by default**.
- A missing required field causes indexing to fail with `ErrFieldValue`.

To make a field optional, attach a default:

```go
Parent  = mddb.String("parent", 32).Default("")
Blocked = mddb.Bool("blocked").Default(false)
Tags    = mddb.Bitset("tags", "bug", "feature").Default() // empty bitset
```

Default values are validated at schema construction time.

## Validation rules

Indexing converts YAML values into the binary encoding. If conversion fails, indexing fails.

Common failures:

- missing required field
- wrong type (e.g., `priority: "high"`)
- enum value not in allowed set
- numeric out of range for the target type
- string longer than `maxLen` in bytes
- list longer than `count` for `StringList`
- unknown token in `Bitset`

Validation failures are reported as `ErrFieldValue` with per-document details.

## Schema versioning

mddb computes a deterministic 64-bit **schema hash** from:

- field names, types, and order
- enum values (in order)
- string lengths / list dimensions
- default values

The schema hash is stored in the cache file (see [slotcache usage](slotcache-usage.md)).

If the schema hash changes, the cache is treated as incompatible and is rebuilt from the `*.mddb.md` files.

## Schema evolution guidance

Safe changes (cache rebuild succeeds automatically):

- add a new optional field (has a default)
- remove a field
- append enum values

Breaking changes (rebuild fails until docs are fixed):

- add a new required field
- remove an enum value that exists in docs
- shorten a `String`/`StringList` max length
- reduce a `StringList` slot count
- change a field type
