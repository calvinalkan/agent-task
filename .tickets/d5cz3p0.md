---
id: d5cz3p0
status: closed
closed: 2026-01-04T04:58:25Z
blocked-by: []
created: 2026-01-04T04:51:36Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add limit and offset to ls command

Add pagination support to `tk ls`:
- `--limit N` - Maximum tickets to display (default: 100)
- `--offset N` - Skip first N tickets (default: 0)

When limit is applied and more tickets exist, print summary line showing total count.

## Design

## Changes to ls.go

Add flags:
- `--limit` (int, default 100)
- `--offset` (int, default 0)

Validation:
- limit >= 0 (0 means no limit? or error? probably 0 = show none)
- offset >= 0

After filtering by status, apply offset/limit to results slice.

If truncated, print footer:
```
... and N more (M total)
```

## Help text update
```
  ls [options]           List tickets
    --status=<status>      Filter by status
    --limit=N              Max tickets to show [default: 100]
    --offset=N             Skip first N tickets [default: 0]
```

## Acceptance Criteria

## Functionality

- [ ] `tk ls` shows at most 100 tickets by default
- [ ] `tk ls --limit=10` shows at most 10 tickets
- [ ] `tk ls --offset=5` skips first 5 tickets
- [ ] `tk ls --limit=10 --offset=5` skips 5, shows next 10
- [ ] When truncated, prints summary: `... and N more (M total)`
- [ ] --status filter applied before limit/offset

## Validation

- [ ] --limit with negative value: error
- [ ] --offset with negative value: error
- [ ] --limit=0 shows no tickets (just summary if any exist)
- [ ] --offset beyond total tickets: shows nothing (no error)

## Invariants

- [ ] Without flags, behavior unchanged for repos with <= 100 tickets
- [ ] offset + limit > total: shows remaining tickets without error
- [ ] Summary line only printed when there are more tickets beyond limit

## Help

- [ ] `tk ls --help` documents --limit and --offset

## Tests

- [ ] ls_test.go covers limit scenarios
- [ ] ls_test.go covers offset scenarios
- [ ] ls_test.go covers combined limit+offset
- [ ] ls_test.go covers edge cases (0, negative, beyond total)
