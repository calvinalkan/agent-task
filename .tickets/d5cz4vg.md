---
id: d5cz4vg
status: closed
closed: 2026-01-04T04:57:47Z
blocked-by: []
created: 2026-01-04T04:54:06Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# start command should print ticket content

When running `tk start <id>`, print the full ticket content after starting it. This saves a follow-up `tk show <id>` call.

## Design

## Changes to start.go

After successfully updating status to in_progress, call `ReadTicket(path)` and print content to stdout.

Output format:
```
Started <id>

<ticket content>
```

Or just print content directly (no "Started" prefix needed since content shows status).

## Acceptance Criteria

## Functionality

- [ ] `tk start <id>` prints ticket content after starting
- [ ] Content shows updated status (in_progress)

## Invariants

- [ ] Error cases still print only error (no content)
- [ ] Exit code unchanged (0 on success, 1 on error)

## Tests

- [ ] start_test.go verifies content is printed on success
