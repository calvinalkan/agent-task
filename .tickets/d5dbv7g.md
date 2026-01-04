---
schema_version: 1
id: d5dbv7g
status: closed
closed: 2026-01-04T19:33:29Z
blocked-by: []
created: 2026-01-04T19:21:02Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add schema_version frontmatter field to tickets

Add a schema_version: 1 field to all ticket frontmatter. This enables future format migrations and allows parsers to handle different ticket versions.

## Design

### Field Position

Add schema_version as the first field after the opening ---:

```yaml
---
id: d5czj08
status: open
...
```

### Code Changes

**ticket.go**
- Add SchemaVersion int field to Ticket struct
- Add SchemaVersion int field to TicketSummary struct  
- Update FormatTicket() to write schema_version: 1 as first frontmatter field
- Update ParseTicketFrontmatter() to parse and validate schema_version (required, positive int, supported version)

**errors.go**
- Add errMissingSchemaVersion and errUnsupportedSchemaVersion

**cache_binary.go**
- Bump cacheVersionNum from 2 to 3 (forces cache rebuild)
- Add SchemaVersion to cached data format

**create.go**
- Set SchemaVersion: 1 when creating new tickets

### Test Updates

- Update all test ticket content to include schema_version: 1
- Add tests for schema_version parsing and validation errors

### Migration

Existing tickets already backfilled with schema_version: 1. Missing schema_version treated as error.

## Acceptance Criteria

### Core Functionality
- [ ] schema_version: 1 written as first field in frontmatter
- [ ] schema_version parsed and validated as required field
- [ ] Missing schema_version produces clear error
- [ ] Unsupported version produces clear error
- [ ] tk create sets schema_version to 1

### Parser Validation
- [ ] schema_version must be present
- [ ] schema_version must be positive integer
- [ ] schema_version must be supported (currently only 1)
- [ ] Empty value produces error

### Tests
- [ ] All test ticket content includes schema_version: 1
- [ ] Tests for schema_version parsing
- [ ] Tests for validation errors

### Cache
- [ ] Cache version bumped to 3
