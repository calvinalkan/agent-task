---
id: d5cypt0
status: closed
closed: 2026-01-04T04:42:22Z
blocked-by: []
created: 2026-01-04T04:24:08Z
type: bug
priority: 1
assignee: Calvin Alkan
---
# Remove t- prefix from ticket ID generation

The ID generation function should not add a "t-" prefix. Just use the timestamp-based component directly.

## Current
`t-d5cypt0`

## Expected
`d5cypt0`

## Acceptance Criteria

- No prefix in generated IDs
- IDs are just the timestamp-based sortable component
