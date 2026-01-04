---
schema_version: 1
id: d5dbcy8
status: open
blocked-by: []
created: 2026-01-04T18:50:33Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add --limit and --offset flags to ready command

The ready command should support --limit and --offset flags for pagination, similar to the ls command. This allows users to page through large lists of actionable tickets.

## Design

Reuse the pattern from ls.go:

### New readyOptions struct
```go
type readyOptions struct {
    limit  int
    offset int
}
```

### New parseReadyFlags function
Similar to parseLsFlags, using pflag. Same validation (non-negative).

### Update cmdReady flow
1. Parse flags with parseReadyFlags (similar to ls)
2. List ALL tickets (NeedAll: true - required for blocker resolution)
3. Filter to ready tickets only (open + all blockers closed)
4. Sort by priority, then ID
5. Apply offset/limit (must be after filter+sort)
6. Output formatted lines

### Why ready can't optimize like ls
`ls` can sometimes apply offset/limit at file-reading level (warm cache, no status filter).

`ready` cannot - it must:
1. Load ALL tickets to build complete statusMap (any ticket could be a blocker)
2. Filter to ready tickets (changes which tickets are in the result set)
3. Sort (changes order)
4. Only then apply offset/limit

Offset/limit semantics: "top N ready tickets by priority", not "first N files".

### Update help output
```
Usage: tk ready [options]

List actionable tickets that can be worked on now.
Shows open tickets with all blockers closed.

Options:
  --limit=N     Max tickets to show [default: 100]
  --offset=N    Skip first N tickets [default: 0]

Output sorted by priority (P1 first), then by ID.
```

### Update main help
Change readyHelp constant to show options exist.

## Acceptance Criteria

## Flag parsing
- [ ] `--limit` and `--offset` show in `tk ready --help`
- [ ] `--limit=N` accepted (default: 100)
- [ ] `--offset=N` accepted (default: 0)
- [ ] `--limit 5` (space-separated) works
- [ ] `--offset 5` (space-separated) works

## Default behavior
- [ ] `tk ready` without flags shows max 100 tickets (default limit)
- [ ] `tk ready --limit=0` shows all tickets (no limit)

## Limit behavior
- [ ] `--limit=2` shows only first 2 ready tickets (after sort)
- [ ] `--limit=N` where N > total ready tickets shows all

## Offset behavior
- [ ] `--offset=1` skips first ready ticket
- [ ] `--offset=N` where N >= total ready tickets → error "offset out of bounds"
- [ ] `--offset=0` is same as no offset

## Combined limit+offset
- [ ] `--limit=2 --offset=1` skips 1, shows next 2
- [ ] Offset applied before limit (skip N, then take M)

## Error handling
- [ ] `--limit=-1` → error "--limit must be non-negative"
- [ ] `--offset=-1` → error "--offset must be non-negative"

## Sorting preserved
- [ ] Output still sorted by priority (P1 first), then by ID
- [ ] Offset/limit applied AFTER sorting

## Idempotency
- [ ] Multiple runs with same flags produce same output

## Integration with warnings
- [ ] Non-existent blocker warnings still output to stderr
- [ ] Exit code 1 if warnings, regardless of limit/offset

## Tests

### New tests for ready_test.go

```go
// TestReadyLimitOffset - table-driven test covering limit/offset combinations
func TestReadyLimitOffset(t *testing.T) {
    // Test cases:
    // - default limit 100
    // - limit 2 shows first 2 (after priority sort)
    // - offset 1 skips first
    // - limit 1 offset 1 shows only second
    // - limit 0 shows all (no limit)
    // - offset beyond total → error "offset out of bounds"
    // - offset equals total → error "offset out of bounds"
    // - negative limit → error "--limit must be non-negative"
    // - negative offset → error "--offset must be non-negative"
    // - offset + limit > total shows remaining
}

func TestReadyLimitPreservesPrioritySort(t *testing.T) {
    // Create P3, P1, P2 tickets
    // Run ready --limit=2
    // Assert: shows P1, P2 (not P3) - proves limit applied AFTER sort
}

func TestReadyOffsetPreservesPrioritySort(t *testing.T) {
    // Create P3, P1, P2 tickets
    // Run ready --offset=1
    // Assert: shows P2, P3 (not P1) - proves offset applied AFTER sort
}

func TestReadyLimitWithBlockedTickets(t *testing.T) {
    // Create 4 open tickets, block 2 of them
    // Run ready --limit=1
    // Assert: shows only 1 ready ticket (not blocked one)
}

func TestReadyOffsetWithBlockedTickets(t *testing.T) {
    // Create 3 open tickets, block 1 of them (highest priority)
    // Run ready --offset=1
    // Assert: offset applied to READY list (after filtering blocked)
}

func TestReadyLimitWithWarnings(t *testing.T) {
    // Create ticket blocked by non-existent ID
    // Run ready --limit=1
    // Assert: warning in stderr, exit code 1, ticket shown in stdout
}

func TestReadyHelpShowsLimitOffset(t *testing.T) {
    // Run ready --help
    // Assert: output contains "--limit" and "--offset" and "100"
}

func TestReadyIdempotentWithLimitOffset(t *testing.T) {
    // Create 5 tickets
    // Run ready --limit=2 --offset=1 twice
    // Assert: both outputs identical
}

func TestReadyEmptyDirWithOffset(t *testing.T) {
    // Empty dir, run ready --offset=1
    // Assert: error "offset out of bounds" (no tickets to offset into)
}

func TestReadyLimitZeroShowsAll(t *testing.T) {
    // Create 5 tickets
    // Run ready --limit=0
    // Assert: all 5 shown
}
```

### Invariants to test

1. **Sorting invariant**: Output is always sorted by priority (P1 first), then by ID, regardless of limit/offset
2. **Filter-before-paginate invariant**: Blocked tickets are filtered BEFORE offset/limit is applied
3. **Offset bounds invariant**: offset >= len(ready_tickets) → error
4. **Negative validation invariant**: negative limit or offset → immediate error before any processing
5. **Idempotency invariant**: same flags + same data = same output
6. **Warning independence**: limit/offset don't suppress or affect warnings
7. **Exit code invariant**: warnings still cause exit code 1, even with limit/offset
