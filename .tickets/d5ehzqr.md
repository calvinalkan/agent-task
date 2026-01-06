---
schema_version: 1
id: d5ehzqr
status: closed
closed: 2026-01-06T15:21:59Z
blocked-by: []
created: 2026-01-06T14:44:47Z
type: task
priority: 3
---
# Unify CLI help display patterns

The CLI layer has inconsistent help patterns that should be unified.

## Design

## Current State

### Issue 1: Three Different Help Display Patterns

| Pattern | Commands | Code Style |
|---------|----------|------------|
| Inline Println | show, start, close, reopen, block, unblock, editor | `o.Println("Usage:..."); o.Println(""); o.Println("...")` |
| pflag buffer | create | `flagSet.Usage()` → buffer → `o.Printf("%s", buf)` |
| Helper function | ls, repair | `printLsHelp(o)`, `printRepairHelp(o)` |

### Issue 2: Help Text Duplication

Every command has TWO versions of help text:

1. Short version (const for main help):
   ```go
   const showHelp = `  show <id>              Show ticket details`
   ```

2. Long version (in command function):
   ```go
   if hasHelpFlag(args) {
       o.Println("Usage: tk show <id>")
       o.Println("")
       o.Println("Display the full contents of a ticket.")
       return nil
   }
   ```

## Constraints

- Keep pflag for POSIX-compliant flag parsing (already works well)
- No new dependencies - this is just internal code organization
- Simple commands (show, start, etc.) shouldn't need pflag if they have no flags

## Proposed Solution

Define command metadata struct that generates both help formats:

```go
type CommandMeta struct {
    Name  string
    Args  string   // "<id>" or "<id> <blocker>" or ""
    Short string   // "Show ticket details"  
    Long  string   // "Display the full contents of a ticket."
}

var showMeta = CommandMeta{
    Name:  "show",
    Args:  "<id>",
    Short: "Show ticket details",
    Long:  "Display the full contents of a ticket.",
}

// Generates: "  show <id>              Show ticket details"
func (m CommandMeta) HelpLine() string { ... }

// Generates full --help output
func (m CommandMeta) PrintHelp(o *IO) { ... }
```

Commands with flags (create, ls, repair) would extend this with their pflag.FlagSet for options display.
