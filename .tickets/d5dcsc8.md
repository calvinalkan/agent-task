---
schema_version: 1
id: d5dcsc8
status: open
blocked-by: []
created: 2026-01-04T20:25:21Z
type: task
priority: 3
assignee: Calvin Alkan
---
# Restructure codebase with internal/ packages and proper module path

Move from flat file structure to organized internal/ packages for better maintainability.

## Design

## Current State
- 39 .go files flat in root
- Module path is just 'tk' (local-only)

## Proposed Structure
```
tk/
├── main.go
├── run.go
├── errors.go
├── cmd_*.go                # Commands stay at root
├── cmd_*_test.go
│
├── internal/
│   ├── ticket/             # Core domain
│   │   ├── ticket.go
│   │   ├── ticket_test.go
│   │   ├── bitmap.go
│   │   └── bitmap_test.go
│   │
│   ├── cache/              # Caching layer
│   │   ├── binary.go
│   │   ├── gob.go
│   │   └── cache_test.go
│   │
│   ├── config/             # Configuration
│   │   ├── config.go
│   │   └── config_test.go
│   │
│   └── lock/               # File locking
│       ├── lock.go
│       └── lock_test.go
```

## Module Path
Change go.mod from:
  module tk
To:
  module github.com/calvinalkan/tk

## Import Style
```go
import (
    "github.com/calvinalkan/tk/internal/config"
    "github.com/calvinalkan/tk/internal/ticket"
    "github.com/calvinalkan/tk/internal/cache"
    "github.com/calvinalkan/tk/internal/lock"
)
```

## Acceptance Criteria

- [ ] go.mod uses github.com/calvinalkan/tk
- [ ] internal/ticket/ contains ticket.go, bitmap.go and tests
- [ ] internal/cache/ contains binary.go, gob.go and tests
- [ ] internal/config/ contains config.go and tests
- [ ] internal/lock/ contains lock.go and tests
- [ ] Commands renamed to cmd_*.go at root
- [ ] All tests pass: go test ./...
- [ ] No circular dependencies
