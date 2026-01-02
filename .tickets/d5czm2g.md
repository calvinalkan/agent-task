---
id: d5czm2g
status: closed
closed: 2026-01-04T05:30:10Z
blocked-by: []
created: 2026-01-04T05:26:34Z
type: bug
priority: 2
assignee: Calvin Alkan
---
# ready command should only show open tickets, not in_progress

The `tk ready` command currently shows both `open` and `in_progress` tickets. It should only show `open` tickets.

Reasoning: `tk ready` answers "What can I start working on?" - tickets that are `in_progress` are already being worked on, so they're not available to pick up.

Ready = open + unblocked

## Design

## Changes to ready.go

Filter to only `open` status, not `open` or `in_progress`.

```go
// Current
if summary.Status != StatusOpen && summary.Status != StatusInProgress {
    continue
}

// New
if summary.Status != StatusOpen {
    continue
}
```

Update help text if it mentions in_progress.

## Acceptance Criteria

- [ ] `tk ready` only shows open tickets
- [ ] in_progress tickets excluded from ready
- [ ] Tests updated
