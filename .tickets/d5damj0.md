---
schema_version: 1
id: d5damj0
status: closed
closed: 2026-01-10T03:42:37Z
blocked-by: [d5dae6r]
created: 2026-01-04T17:58:32Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# tk start: error if ticket or ancestor is blocked

## Design

## Summary

Update `tk start` to error if the ticket is blocked (directly or via ancestor).

## Current behavior

`tk start` only checks:
- Status is `open`

## New behavior

`tk start` checks:
- Status is `open`
- Own `blocked-by` all closed
- All ancestors' `blocked-by` all closed

Error message if blocked:
```
error: cannot start d5abc.1.1: blocked by d5xyz (via ancestor d5abc)
```

Or if directly blocked:
```
error: cannot start d5abc: blocked by [d5xyz, d5def]
```

## Rationale

Prevents starting work that cannot be completed. Use `tk ready` to find what can actually be worked on.

## Acceptance Criteria

- [ ] Error if own `blocked-by` has open tickets
- [ ] Error if any ancestor is blocked
- [ ] Error message shows what's blocking
- [ ] Error message shows if blocking is inherited (via ancestor)
- [ ] Still works for unblocked tickets
