---
schema_version: 1
id: d5dk26g
status: open
blocked-by: []
created: 2026-01-05T03:33:46Z
type: chore
priority: 2
assignee: Calvin Alkan
---
# Remove diagOut parameter from ListTickets

The diagOut io.Writer parameter in ListTickets is unnecessary. The function should just return errors instead of writing diagnostic messages to a separate writer.

Changes needed:
- Remove diagOut parameter from ListTickets signature
- Remove diagnostic fprintln calls or convert critical ones to returned errors
- Update all callers (tests, repair.go) to use the simplified signature
