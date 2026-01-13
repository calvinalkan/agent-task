---
schema_version: 1
id: d5dbcy8
status: closed
closed: 2026-01-10T03:43:34Z
blocked-by: []
created: 2026-01-04T18:50:33Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add --limit flag to ready command

The ready command should support a --limit flag to cap output, similar to the ls command.

## Acceptance Criteria

- [x] `--limit` shows in `tk ready --help`
- [x] `--limit=N` accepted (0 = no limit)
- [x] `--limit 5` (space-separated) works
- [x] `tk ready` without flags shows all tickets
- [x] `--limit=2` shows only first 2 ready tickets (after sort)
- [x] Output still sorted by priority (P1 first), then by ID
