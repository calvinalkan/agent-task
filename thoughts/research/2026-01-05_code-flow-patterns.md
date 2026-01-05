---
date: 2026-01-05T14:07:33+01:00
researcher: Claude
git_commit: e10fd05a8f2a76b31e5cbe4e38692ed5c2157a4b
branch: master
repository: tk
topic: "CLI code flow patterns: boot → commands → ticket operations → filesystem"
tags: [research, codebase, cli, patterns, architecture]
status: complete
last_updated: 2026-01-05
last_updated_by: Claude
last_updated_note: "Added internal duplication analysis for ticket.go; Added dead code analysis for cache.go and cache_gob.go"
---

# Research: CLI Code Flow Patterns

**Date**: 2026-01-05T14:07:33+01:00
**Researcher**: Claude
**Git Commit**: e10fd05a8f2a76b31e5cbe4e38692ed5c2157a4b
**Branch**: master
**Repository**: tk

## Research Question

How does code flow from CLI boot to CLI commands to ticket operations and file system writing? Document the different patterns used across commands to understand current inconsistencies.

## Summary

The tk CLI uses a simple boot → dispatch → command → ticket package → filesystem flow. However, there are **four distinct patterns** for command implementation, **two patterns** for help display, **two patterns** for flag parsing libraries, and **three patterns** for ticket file operations. These inconsistencies appear across the 11 CLI commands.

## Detailed Findings

### 1. Boot Sequence

**Entry Point**: `cmd/tk/main.go:11-20`

```
main() 
  → collect env vars from os.Environ()
  → call cli.Run(stdin, stdout, stderr, args, env)
  → os.Exit with return code
```

The main function acts as a thin wrapper that passes OS abstractions to the CLI layer.

**Dispatcher**: `internal/cli/run.go:21-90`

```
Run()
  → parseGlobalFlags() for -C, --cwd, -c, --config, --ticket-dir
  → os.Getwd() for default workDir
  → ticket.LoadConfig() merges global → project → CLI configs
  → switch on command name → call cmd* function
```

### 2. Command Signature Patterns

All commands share the same function signature:

```go
func cmd*(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int
```

**Exception**: `cmdEditor` has an extra `env` parameter:
```go
func cmdEditor(..., env map[string]string) int
```

**Location**: `internal/cli/editor.go:56`

### 3. Help Display Patterns

**Pattern A: hasHelpFlag() early return with manual printing**
Used by: `show`, `start`, `close`, `reopen`, `block`, `unblock`, `ready`, `editor`

```go
if hasHelpFlag(args) {
    fprintln(out, "Usage: tk <cmd> <id>")
    fprintln(out, "")
    fprintln(out, "Description...")
    return 0
}
```

**Pattern B: pflag.FlagSet with flagSet.Usage callback**
Used by: `create`, `ls`, `repair`

```go
flagSet := flag.NewFlagSet("create", flag.ContinueOnError)
flagSet.SetOutput(errOut)
flagSet.Usage = func() {
    w := flagSet.Output()
    fprintf(w, "Usage: tk create <title> [options]\n\n")
    ...
    flagSet.PrintDefaults()
}

if hasHelpFlag(args) {
    flagSet.SetOutput(out)  // Switch to stdout for help
    flagSet.Usage()
    return 0
}
```

### 4. Flag Parsing Patterns

**Pattern A: No flag parsing (positional args only)**
Used by: `show`, `start`, `close`, `reopen`, `editor`

```go
// internal/cli/show.go:20-24
if len(args) == 0 {
    fprintln(errOut, "error:", ticket.ErrIDRequired)
    return 1
}
ticketID := args[0]
```

**Pattern B: Manual slice parsing for multiple positional args**
Used by: `block`, `unblock`

```go
// internal/cli/block.go:25-35
if len(args) == 0 {
    fprintln(errOut, "error:", ticket.ErrIDRequired)
    return 1
}
if len(args) < 2 {
    fprintln(errOut, "error:", ticket.ErrBlockerIDRequired)
    return 1
}
ticketID := args[0]
blockerID := args[1]
```

**Pattern C: spf13/pflag with flags parsed into local variables**
Used by: `create`, `ls`

```go
// internal/cli/create.go:36-44
flagSet := flag.NewFlagSet("create", flag.ContinueOnError)
description := flagSet.StringP("description", "d", "", "Description text")
...
parseErr := flagSet.Parse(args)
```

**Pattern D: spf13/pflag with flags parsed into struct**
Used by: `ls` (unique - uses `lsOptions` struct)

```go
// internal/cli/ls.go:16-22
type lsOptions struct {
    status     string
    priority   int
    ticketType string
    limit      int
    offset     int
}
```

**Pattern E: spf13/pflag with boolean mode flags**
Used by: `repair`

```go
// internal/cli/repair.go:25-28
allFlag := flagSet.Bool("all", false, "Repair all tickets")
dryRun := flagSet.Bool("dry-run", false, "Show what would be fixed")
rebuildCache := flagSet.Bool("rebuild-cache", false, "Rebuild cache")
```

### 5. Ticket Directory Resolution Pattern

All commands use the same pattern for resolving the ticket directory:

```go
// Appears in every command
ticketDir := cfg.TicketDir
if !filepath.IsAbs(ticketDir) {
    ticketDir = filepath.Join(workDir, ticketDir)
}
```

**Location examples**:
- `internal/cli/create.go:97-100`
- `internal/cli/show.go:29-32`
- `internal/cli/ls.go:41-44`

### 6. Ticket Existence Check Pattern

Commands that operate on existing tickets use:

```go
if !ticket.Exists(ticketDir, ticketID) {
    fprintln(errOut, "error:", ticket.ErrTicketNotFound, ticketID)
    return 1
}
```

Used by: `show`, `start`, `close`, `reopen`, `block`, `unblock`, `editor`, `repair`

### 7. File Operation Patterns

**Pattern A: Direct WriteTicketAtomic (new tickets)**
Used by: `create`

```go
// internal/cli/create.go:131-138
ticketID, ticketPath, writeErr := ticket.WriteTicketAtomic(ticketDir, &tkt)
```

Flow:
```
WriteTicketAtomic()
  → os.MkdirAll() for ticket dir
  → WithLock() on base ID
  → GenerateUniqueID() inside lock
  → WriteTicket() → atomic.WriteFile()
```

**Pattern B: WithTicketLock with content transformation**
Used by: `start`, `close`, `reopen`, `block`, `unblock`

```go
// internal/cli/start.go:39-51
err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
    status, statusErr := ticket.GetStatusFromContent(content)
    if statusErr != nil {
        return nil, fmt.Errorf("reading status: %w", statusErr)
    }
    if status != ticket.StatusOpen {
        return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotOpen, status)
    }
    return ticket.UpdateStatusInContent(content, ticket.StatusInProgress)
})
```

Flow:
```
WithTicketLock()
  → acquireLock() on .locks/<file>.lock
  → os.ReadFile()
  → handler(content) → returns new content
  → atomic.WriteFile() if new content != nil
  → lock.release()
```

**Pattern C: UpdateTicketBlockedByLocked (direct field update)**
Used by: `repair`

```go
// internal/cli/repair.go:119
err = ticket.UpdateTicketBlockedByLocked(path, newBlockedBy)
```

This is a specialized version of Pattern B for blocked-by field only.

**Pattern D: Read-only (no writes)**
Used by: `show`, `ls`, `ready`, `editor`

```go
// internal/cli/show.go:40-45
content, err := ticket.ReadTicket(path)
```

### 8. Cache Update Patterns

**Pattern A: Explicit cache update after write**
Used by: `create`, `start`, `close`, `reopen`, `block`, `unblock`, `repair`

```go
// internal/cli/create.go:140-149
summary, parseErr := ticket.ParseTicketFrontmatter(ticketPath)
if parseErr != nil {
    fprintln(errOut, "error:", parseErr)
    return 1
}

cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
if cacheErr != nil {
    fprintln(errOut, "error:", cacheErr)
    return 1
}
```

**Pattern B: Full cache rebuild**
Used by: `repair --rebuild-cache`

```go
// internal/cli/repair.go:37-50
results, err := ticket.BuildCacheParallelLocked(ticketDir, nil)
```

**Pattern C: Implicit cache handling via ListTickets**
Used by: `ls`, `ready`

```go
// internal/cli/ls.go:47-52
results, err := ticket.ListTickets(ticketDir, listOpts, errOut)
```

`ListTickets` internally handles cache loading, validation, and reconciliation.

### 9. Output Patterns

**Pattern A: Single value output**
Used by: `create` (prints ticket ID)

```go
fprintln(out, ticketID)
```

**Pattern B: Confirmation message**
Used by: `start`, `close`, `reopen`, `block`, `unblock`, `repair`

```go
fprintln(out, "Started", ticketID)
fprintln(out, "Closed", ticketID)
fprintln(out, "Blocked", ticketID, "by", blockerID)
```

**Pattern C: Full content dump**
Used by: `show`, `start` (after confirmation)

```go
_, _ = io.WriteString(out, content)
```

**Pattern D: Formatted list**
Used by: `ls`, `ready`

```go
// internal/cli/ls.go:161-172
func formatTicketLine(summary *ticket.Summary) string {
    var builder strings.Builder
    builder.WriteString(summary.ID)
    builder.WriteString(" [")
    builder.WriteString(summary.Status)
    builder.WriteString("] - ")
    builder.WriteString(summary.Title)
    ...
}
```

### 10. Error Handling Patterns

**Pattern A: Return 1 with error message**
Used universally:

```go
fprintln(errOut, "error:", err)
return 1
```

**Pattern B: Warnings that don't fail**
Used by: `ls`, `ready`, `repair`

```go
// internal/cli/ls.go:77-82
for _, result := range results {
    if result.Err != nil {
        fprintln(errOut, "warning:", result.Path+":", result.Err)
        hasErrors = true
        continue
    }
    ...
}
```

### 11. Ticket Package Layer Architecture

```
internal/ticket/
├── ticket.go        # Core types, parsing, ID generation
├── errors.go        # All error definitions
├── config.go        # Configuration loading
├── lock.go          # File locking (syscall.Flock)
├── cache.go         # Binary mmap cache (production)
├── cache_gob.go     # Gob cache (unused, Windows backup)
├── cache_write_through.go  # Cache update helpers
```

**Key Types**:
- `Ticket`: Full ticket with all fields (for creation)
- `Summary`: Frontmatter-only data (for listing)
- `Result`: Summary + Path + Err (for list operations)
- `CacheEntry`: Mtime + Summary (for cache)
- `BinaryCache`: mmap'd read-only cache

### 12. Locking Architecture

Lock files are stored in `.tickets/.locks/` subdirectory:

```
.tickets/
├── .locks/
│   ├── abc123.md.lock    # Lock for ticket file
│   └── .cache.lock       # Lock for cache file
├── .cache                # Binary cache
├── abc123.md             # Ticket file
└── def456.md             # Ticket file
```

**Lock acquisition flow** (`internal/ticket/lock.go:58-117`):
```
acquireLockWithTimeout()
  → os.MkdirAll(.locks/)
  → os.OpenFile(.lock file)
  → syscall.Fstat() to get inode
  → syscall.Flock(LOCK_EX) with timeout goroutine
  → syscall.Stat() to verify inode matches (race protection)
  → return fileLock
```

## Code References

### Entry Points
- `cmd/tk/main.go:11-20` - Main entry point
- `internal/cli/run.go:21-90` - Command dispatcher

### Commands by Pattern Type

**Simple positional (Pattern A)**:
- `internal/cli/show.go:14-50` - show command
- `internal/cli/start.go:14-77` - start command
- `internal/cli/close.go:14-82` - close command
- `internal/cli/reopen.go:14-86` - reopen command
- `internal/cli/editor.go:55-100` - editor command

**Two positional args (Pattern B)**:
- `internal/cli/block.go:16-93` - block command
- `internal/cli/unblock.go:14-81` - unblock command

**pflag with options (Patterns C/D/E)**:
- `internal/cli/create.go:31-155` - create command (Pattern C)
- `internal/cli/ls.go:24-180` - ls command (Pattern C+D)
- `internal/cli/repair.go:18-195` - repair command (Pattern E)

**No args needed**:
- `internal/cli/ready.go:18-100` - ready command

### Ticket Operations
- `internal/ticket/ticket.go:177-204` - WriteTicketAtomic
- `internal/ticket/ticket.go:285-308` - UpdateStatusInContent
- `internal/ticket/lock.go:36-54` - WithTicketLock
- `internal/ticket/cache.go:434-501` - UpdateCacheEntry

### File System Layer
- `internal/fs/fs.go` - FS interface (not currently used by CLI)
- `internal/fs/real.go` - Real implementation
- Uses `github.com/natefinch/atomic` for atomic writes
- Uses `syscall.Flock` for locking (Linux-specific)

## Architecture Documentation

### Command Implementation Structure

Each command follows this general structure:
1. Help check (either manual or via pflag)
2. Flag/argument parsing
3. Ticket directory resolution
4. Ticket existence check (for existing tickets)
5. Ticket operation (read/write via ticket package)
6. Cache update (if write operation)
7. Output and return code

### Data Flow Diagram

```
CLI Layer (internal/cli/)
    │
    ├── Flag Parsing
    │   ├── hasHelpFlag() for simple commands
    │   └── spf13/pflag for complex commands
    │
    ├── Ticket Dir Resolution
    │   └── cfg.TicketDir + filepath.Join(workDir, ...)
    │
    └── Delegates to Ticket Layer
            │
Ticket Layer (internal/ticket/)
    │
    ├── High-Level Ops
    │   ├── WriteTicketAtomic() - new tickets
    │   ├── WithTicketLock() - read-modify-write
    │   └── ListTickets() - cached listing
    │
    ├── Content Manipulation
    │   ├── *FromContent() - read field from bytes
    │   ├── Update*InContent() - modify bytes
    │   └── ParseTicketFrontmatter() - full parse
    │
    ├── Locking (lock.go)
    │   └── syscall.Flock via .locks/ subdir
    │
    └── Caching (cache*.go)
        ├── BinaryCache - mmap read
        └── writeBinaryCache - atomic write

File System
    │
    ├── os package (direct use)
    │   ├── os.ReadFile
    │   ├── os.Stat
    │   └── os.MkdirAll
    │
    └── github.com/natefinch/atomic
        └── atomic.WriteFile
```

---

## Internal Duplication in ticket.go

The `internal/ticket/ticket.go` file (1100+ lines) contains significant internal duplication across several categories.

### 13. Frontmatter Iteration Pattern (6 instances)

The same loop structure for iterating through YAML frontmatter lines appears in **6 different functions**:

```go
lines := strings.Split(string(content), "\n")
inFrontmatter := false

for _, line := range lines {
    if line == frontmatterDelimiter {
        if inFrontmatter {
            break // End of frontmatter
        }
        inFrontmatter = true
        continue
    }

    if inFrontmatter && strings.HasPrefix(line, "<field>: ") {
        // do something with the field
    }
}
```

**Instances**:

| Function | Line | Field Accessed | Operation |
|----------|------|----------------|-----------|
| `GetStatusFromContent` | 267-285 | `status: ` | Read field value |
| `UpdateStatusInContent` | 298-324 | `status: ` | Replace line |
| `AddFieldToContent` | 365-395 | `status: ` | Find insertion point |
| `RemoveFieldFromContent` | 420-450 | `<field>: ` | Delete line |
| `GetBlockedByFromContent` | 770-792 | `blocked-by: ` | Read field value |
| `UpdateBlockedByInContent` | 808-836 | `blocked-by: ` | Replace line |

Each function re-implements:
- Line splitting: `strings.Split(string(content), "\n")`
- Frontmatter state tracking: `inFrontmatter` boolean
- Delimiter detection: `if line == frontmatterDelimiter`
- Field prefix matching: `strings.HasPrefix(line, "field: ")`
- Line rejoining: `strings.Join(lines, "\n")`

### 14. Read-File-Then-Call-FromContent Pattern (2 instances)

Two functions exist solely to read a file and delegate to a `*FromContent` function:

**Pattern A**: `ReadTicketStatus` wraps `GetStatusFromContent`
```go
// internal/ticket/ticket.go:288-296
func ReadTicketStatus(path string) (string, error) {
    content, err := os.ReadFile(path)
    if err != nil {
        return "", fmt.Errorf("reading ticket: %w", err)
    }
    return GetStatusFromContent(content)
}
```

**Pattern B**: `ReadTicketBlockedBy` wraps `GetBlockedByFromContent`
```go
// internal/ticket/ticket.go:795-804
func ReadTicketBlockedBy(path string) ([]string, error) {
    content, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("reading ticket: %w", err)
    }
    return GetBlockedByFromContent(content)
}
```

Both follow identical structure: read file → handle error → call `*FromContent`.

### 15. Deprecated Unlocked + Locked Function Pairs (4 pairs)

Four field operations exist in both deprecated unlocked and current locked versions:

| Unlocked (Deprecated) | Locked | Lines |
|-----------------------|--------|-------|
| `UpdateTicketStatus` | `UpdateTicketStatusLocked` | 327-345, 348-352 |
| `AddTicketField` | `AddTicketFieldLocked` | 398-415, 418-422 |
| `RemoveTicketField` | `RemoveTicketFieldLocked` | 453-470, 473-477 |
| `UpdateTicketBlockedBy` | `UpdateTicketBlockedByLocked` | 839-856, 859-863 |

**Unlocked pattern** (deprecated, 4 instances):
```go
func UpdateTicketStatus(path, newStatus string) error {
    content, err := os.ReadFile(path)
    if err != nil {
        return fmt.Errorf("reading ticket: %w", err)
    }
    newContent, err := UpdateStatusInContent(content, newStatus)
    if err != nil {
        return err
    }
    writeErr := atomic.WriteFile(path, strings.NewReader(string(newContent)))
    if writeErr != nil {
        return fmt.Errorf("writing ticket: %w", writeErr)
    }
    return nil
}
```

**Locked pattern** (current, 4 instances):
```go
func UpdateTicketStatusLocked(path, newStatus string) error {
    return WithTicketLock(path, func(content []byte) ([]byte, error) {
        return UpdateStatusInContent(content, newStatus)
    })
}
```

The deprecated functions all share identical structure:
1. `os.ReadFile(path)`
2. Error wrapping with `"reading ticket: %w"`
3. Call `*InContent` function
4. `atomic.WriteFile(path, strings.NewReader(string(newContent)))`
5. Error wrapping with `"writing ticket: %w"`

### 16. Duplicate Type Validation Functions

Two functions validate ticket types with identical logic:

```go
// internal/ticket/ticket.go:119-121
func IsValidType(ticketType string) bool {
    return slices.Contains(validTypes, ticketType)
}

// internal/ticket/ticket.go:521-523
func IsValidTicketType(ticketType string) bool {
    return slices.Contains(validTypes, ticketType)
}
```

Both check against the same `validTypes` slice. The only difference is the function name.

### 17. Field Read vs Update Function Pairs

For each field that can be read and updated, there are parallel function pairs:

| Field | Read Function | Update Function |
|-------|---------------|-----------------|
| status | `GetStatusFromContent` | `UpdateStatusInContent` |
| blocked-by | `GetBlockedByFromContent` | `UpdateBlockedByInContent` |

Each pair duplicates the frontmatter iteration logic but with different operations (return value vs replace line).

### 18. Error Message Formatting Patterns

Multiple locations use the same error wrapping pattern:

```go
return fmt.Errorf("reading ticket: %w", err)   // 5 occurrences
return fmt.Errorf("writing ticket: %w", err)   // 4 occurrences
return fmt.Errorf("opening ticket: %w", err)   // 1 occurrence
return fmt.Errorf("scanning ticket: %w", err)  // 1 occurrence
```

### 19. Content-to-Lines-to-Content Pattern

Multiple functions convert content to lines and back:

```go
// At start of function:
lines := strings.Split(string(content), "\n")

// At end of function:
return []byte(strings.Join(lines, "\n")), nil
```

Found in:
- `UpdateStatusInContent` (lines 298, 323)
- `AddFieldToContent` (lines 365, 393)
- `RemoveFieldFromContent` (lines 420, 448)
- `UpdateBlockedByInContent` (lines 808, 835)

### 20. ParseTicketFrontmatter Field Handling Pattern

Inside `ParseTicketFrontmatter` (lines 558-750), each field case follows the same pattern:

```go
case "<field>":
    if value == "" {
        return Summary{}, fmt.Errorf("%w: <field> (empty)", errInvalidFieldValue)
    }
    // Optional: additional validation
    summary.<Field> = value
    has<Field> = true
```

This pattern repeats for: `schema_version`, `id`, `status`, `type`, `priority`, `created`, `blocked-by`, `assignee`, `closed`.

### Summary of Duplication in ticket.go

| Duplication Type | Count | Lines Affected (approx) |
|------------------|-------|-------------------------|
| Frontmatter iteration loops | 6 | ~120 lines |
| Read-file wrappers | 2 | ~16 lines |
| Unlocked/Locked pairs | 4 pairs (8 functions) | ~80 lines |
| Duplicate type validators | 2 | ~6 lines |
| Read/Update field pairs | 2 pairs | ~60 lines |
| Content↔lines conversion | 4 | ~16 lines |
| **Total estimated duplication** | | **~300 lines** |

## Open Questions

1. The `internal/fs/` package defines an FS interface but it's not used by the CLI or ticket packages - is this intended for future use or dead code?

2. The cache_gob.go file is marked as "unused" but contains exported functions like `LoadCache()`, `SaveCache()`, `DeleteCache()` - is this intentional for Windows support?

3. The `editor` command is the only one that receives `env` in its signature - should other commands that might need env access follow this pattern?

4. Why do both `IsValidType` and `IsValidTicketType` exist when they do the same thing?

5. Should the deprecated unlocked functions be removed, or are they kept for backwards compatibility?

6. Could the frontmatter iteration pattern be abstracted into a single helper that takes a field name and operation callback?

---

## Dead Code Analysis: cache.go and cache_gob.go

Analysis of which cache functions are actually used in production code vs only in tests.

### 21. Dead Code in cache.go

**Completely Dead (only used in tests)**:

| Function/Method | Lines | Reason Dead |
|-----------------|-------|-------------|
| `BinaryCache.Lookup` | 174-193 | Only called from test files |
| `bc.binarySearch` | 285-306 | Only called by `Lookup` |
| `bc.readFilename` | 308-320 | Only called by `binarySearch` |
| `compareStrings` | 982-992 | Only called by `binarySearch` |
| `DeleteCacheEntry` | 852-877 | Only called from test files |

**Dead code chain**: `Lookup` → `binarySearch` → `readFilename` → `compareStrings`

These ~80 lines implement a binary search lookup by filename that is never used in production. The production code uses `FilterEntries` + `GetEntryByIndex` instead.

### 22. Dead Code in cache_gob.go

**Completely Dead (entire file only used in tests)**:

| Function | Lines | Notes |
|----------|-------|-------|
| `LoadCache` | 40-68 | Gob-based cache load - never used |
| `SaveCache` | 72-89 | Gob-based cache save - never used |
| `Cache` type | 21-24 | Gob-based cache struct - never used |

The file header says "Currently unused - the binary mmap cache is used instead. Kept for potential Windows support."

**Only `DeleteCache` is used** (from `cache_write_through.go:15`) - this function removes the entire cache file, not a single entry.

### 23. Production Cache Usage Flow

The actual production code path for cache operations:

**Reading (ListTickets in ticket.go)**:
```
ListTickets()
  → LoadBinaryCache()           ✓ USED
  → cache.FilterEntries()       ✓ USED  
  → cache.GetEntryByIndex()     ✓ USED
  → cache.Close()               ✓ USED
```

**Writing (UpdateCacheAfterTicketWrite)**:
```
UpdateCacheAfterTicketWrite()
  → UpdateCacheEntry()          ✓ USED
     → LoadBinaryCache()
     → cacheEntriesAsRawMap()   ✓ USED (internal)
     → reconcileRawCacheEntries() ✓ USED (internal)
     → writeBinaryCacheRaw()    ✓ USED (internal)
  → DeleteCache() [on error]    ✓ USED (from cache_gob.go)
```

**Cache rebuild (buildCacheParallel in ticket.go)**:
```
buildCacheParallel()
  → writeBinaryCache()          ✓ USED
     → encodeSummaryData()      ✓ USED (internal)
     → writeBinaryCacheRaw()    ✓ USED (internal)
```

### 24. Summary of Dead Code

| File | Dead Functions | Dead Lines (approx) |
|------|----------------|---------------------|
| cache.go | 5 functions | ~80 lines |
| cache_gob.go | 2 functions + 1 type | ~55 lines |
| **Total** | 7 functions + 1 type | **~135 lines** |

### 25. Functions Used Only by Dead Code

The following internal functions exist only to support `Lookup`:

- `binarySearch` - binary search implementation
- `readFilename` - reads just the filename from an entry
- `compareStrings` - string comparison for binary search

These could be removed along with `Lookup`.

### 26. Test-Only Exports

These exports exist only for test files:

| Export | File | Used By |
|--------|------|---------|
| `Lookup` | cache.go | cache_test.go, cache_write_through_test.go |
| `DeleteCacheEntry` | cache.go | cache_test.go |
| `LoadCache` | cache_gob.go | cache_gob_test.go |
| `SaveCache` | cache_gob.go | cache_gob_test.go |
| `Cache` | cache_gob.go | cache_gob_test.go |

### 27. Remaining Questions

1. Should `Lookup` and its supporting functions be removed, or kept for potential future use?
2. Should `DeleteCacheEntry` be removed since only `DeleteCache` (full cache removal) is used?
3. Should cache_gob.go be removed entirely, or kept as documented for Windows support?
4. If Windows support is needed, should it be implemented properly with build tags instead of dead code?
