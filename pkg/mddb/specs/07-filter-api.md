# Filter API

`Filter(...)` returns matches using only the cache (no document file I/O).

`Get(key)` always reads the `.mddb.md` file and is the authoritative read path.

## Filter options

```go
type FilterOpts struct {
    Reverse bool // false=ascending, true=descending
    Offset  int  // skip first N matches
    Limit   int  // 0 = no limit
}
```

- `Offset` and `Limit` apply after ordering.
- If `Offset` exceeds the number of results, return `ErrOffsetOutOfBounds`.

## Matcher forms

The `matcher` parameter MAY be:

- a field matcher expression (field-based schema)
- a callback predicate
- `nil` (match all)

Examples:

```go
// Field matchers
Status.Eq("open")
Priority.Gte(2)
Status.Eq("open").And(Priority.Gte(2))

// Callback
func(key string, idx TicketIndex) bool {
    return idx.Status == StatusOpen && idx.Priority >= 2
}

// Match all
nil
```

## Boolean chaining

Chaining is left-to-right; nesting controls precedence:

```go
// (A AND B) OR C
A.And(B).Or(C)

// A AND (B OR C)
A.And(B.Or(C))
```

## Ordering

mddb needs a deterministic order to make `Offset/Limit` meaningful.

Two plausible ordering modes exist:

1. **Key order** (lexicographic by key bytes)
2. **Slot order** (cache slot-id order; typically insertion order)

The original rough design doc assumes **key order** for pagination.

Because slotcache is optimized for sequential slot scans, key-order pagination typically requires sorting the match set by key.

This spec therefore defines:

- **Default:** Key order (lexicographic by key bytes).
- **Implementation note:** It is acceptable to implement this as:
  1) scan slots and collect matches
  2) stable-sort matches by key
  3) apply Reverse/Offset/Limit

If you want a "no-sort" fast path, define an additional option in the public API (e.g., `Order: SlotOrder`).

## Transaction visibility

Within an active transaction in the same process:

- `Filter` MUST reflect buffered creates/updates/deletes (read-your-writes).
- `Filter` MUST NOT expose uncommitted writes to other processes.

How the overlay is implemented is an internal detail (it can be done by merging a tx buffer with the base cache results).
