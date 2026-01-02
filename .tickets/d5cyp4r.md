---
id: d5cyp4r
status: closed
closed: 2026-01-04T04:42:28Z
blocked-by: []
created: 2026-01-04T04:22:43Z
type: task
priority: 1
assignee: Calvin Alkan
---
# Migrate deps to blocked-by

Rename deps field to blocked-by for clarity. Update all existing tickets and code.

## Design

## Changes

### File Format
```yaml
# Before
blocked-by: [abc-123, def-456]

# After  
blocked-by: [abc-123, def-456]
```

### CLI Flag
```
# Before
tk create "Title" --dep abc-123

# After
tk create "Title" --blocked-by abc-123
```

### Code Changes
- ticket.go: rename Deps → BlockedBy in struct
- ticket.go: update FormatTicket to write `blocked-by:`
- ticket.go: update ParseTicketFrontmatter to read `blocked-by:`
- create.go: rename --dep flag to --blocked-by
- ls.go: update display format

### Migration
- Update all .tickets/*.md files: `deps:` → `blocked-by:`
- One-time sed or script

### Future
- Can compute inverse ("blocks") at query time by scanning all tickets

## Acceptance Criteria

- blocked-by field replaces deps in file format
- --blocked-by flag replaces --dep in create command
- All existing tickets migrated
- ls shows blocked-by correctly
- Parser reads blocked-by
- Writer writes blocked-by
- Old deps field causes parse error (no backward compat)
