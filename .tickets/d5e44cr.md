---
schema_version: 1
id: d5e44cr
status: open
blocked-by: []
created: 2026-01-05T22:58:59Z
type: chore
priority: 4
assignee: Calvin Alkan
---
# Review: Path traversal protection for ticket IDs

Security review flagged that ticket IDs like `../victim` could escape `.tickets/` via `filepath.Join(ticketDir, ticketID+".md")` in `ticket.Exists`/`ticket.Path`.

**Suggested fix:** Add deterministic CLI tests that:
- `show ../victim` fails and doesn't leak file contents
- `start/close/reopen ../victim` fail and don't modify files outside .tickets
- `block/unblock` with traversal args fail similarly

**Threat model consideration:** This is a local ticketing system. If an agent can run arbitrary commands, it already has file system access. The path traversal protection may be defense-in-depth but not critical given the trust model.

**Decision needed:** Is this worth implementing or can we accept the risk?
