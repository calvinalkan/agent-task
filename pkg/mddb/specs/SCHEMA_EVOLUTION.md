# Schema evolution (non-normative)

This document provides **guidance and examples** for evolving index schemas. It is **non-normative**; see [006-index-schema.md](./006-index-schema.md) for normative encoding rules.

## Required vs optional fields

All indexed fields are required by default. If a field is missing, indexing fails.

Use defaults to make a field optional:

```go
// Required - must be present in frontmatter
Status   = mddb.Enum("status", "open", "in_progress", "closed")
Priority = mddb.Uint8("priority")

// Optional - uses default when missing
Parent   = mddb.String("parent", 32).Default("")
Tags     = mddb.Bitset("tags", "bug", "feature").Default()  // empty bitset
Blocked  = mddb.Bool("blocked").Default(false)
```

### Default value validation examples

Defaults should be validated at schema construction. Examples of invalid defaults:

```go
mddb.Enum("status", "open", "closed").Default("invalid")  // not in enum
mddb.Uint8("priority").Default(300)                        // exceeds uint8
mddb.String("parent", 32).Default(strings.Repeat("x", 64)) // exceeds maxLen
```

## Schema versioning via hash

Implementations typically compute a stable hash over:

- field names, types, and order
- enum/bitset values (including order)
- string length parameters
- default values

This hash is included in cache compatibility (e.g., `slotcache.UserVersion`). If the hash changes, the cache is rebuilt.

## Safe vs breaking changes

Some schema changes are safe and can be handled automatically; others require fixing documents before rebuild succeeds.

**Safe changes (auto-handled):**

| Change | Behavior |
|--------|----------|
| New optional field (has `.Default()`) | Use default for existing docs |
| Removed field | Field no longer indexed (frontmatter preserved in docs) |
| New enum value (appended) | Existing indices unchanged |
| Bitset: removed value | Bit no longer set, others still work |

**Breaking changes (rebuild errors with details):**

| Change | Example Error |
|--------|---------------|
| New required field | `doc "0005": field "assignee" required but missing` |
| Enum: removed value | `doc "0005": field "status" unknown value "archived"` |
| String: shorter maxLen | `doc "0005": field "parent" value (50 bytes) exceeds maxLen 32` |
| StringList: fewer slots | `doc "0005": field "tags" has 5 items, max 4` |
| StringList: shorter item len | `doc "0005": field "tags[2]" value (24 bytes) exceeds maxLen 16` |
| Field type changed | `doc "0005": field "priority" type mismatch` |

Breaking changes require fixing affected documents (or reverting the schema) before rebuild succeeds.

## Field validation examples

Indexing fails with descriptive errors if YAML doesn’t fit the schema. Examples:

| Field Type | Error Condition | Example Error |
|------------|-----------------|---------------|
| Any | Missing required field | `field "status": required but missing` |
| `Enum` | Unknown value | `field "status": unknown value "pending", valid: [open, in_progress, closed]` |
| `Uint8` | Out of range | `field "priority": value 300 exceeds uint8 range` |
| `String` | Too long | `field "parent": value "..." (42 bytes) exceeds max 32 bytes` |
| `Bitset` | Unknown value | `field "tags": unknown value "oops", valid: [bug, feature, urgent]` |
| `StringList` | Too many items | `field "blocked_by": 5 items exceeds max 4` |
| `StringList` | Item too long | `field "blocked_by[2]": value "..." (24 bytes) exceeds max 16 bytes` |

Fewer items than slots is OK (remaining slots are empty/zero). Validation errors surface during `Create`, `Update`, or `Rebuild`.
