---
schema_version: 1
id: d5d9ab8
status: closed
closed: 2026-01-04T17:59:29Z
blocked-by: []
created: 2026-01-04T16:28:29Z
type: feature
priority: 3
assignee: Calvin Alkan
---
# Add editor command to open ticket in editor

Add `tk editor <id>` command that opens the ticket file in the user's preferred editor.

## Design

## Editor Resolution

Check in order, use first that exists (via exec.LookPath):
1. `config.Editor` (from .tk.json)
2. `$EDITOR`
3. `vi`
4. `nano`
5. error: no editor found

## Config Addition

```json
{
  "ticket_dir": ".tickets",
  "editor": "nvim"  // optional
}
```

## New file: editor.go

```go
func cmdEditor(out, errOut io.Writer, cfg *Config, workDir string, args []string) int
```

1. Parse args, require exactly 1 ID
2. Check ticket exists: `<ticketDir>/<id>.md`
3. Resolve editor (config -> $EDITOR -> vi -> nano -> error)
4. exec.Command(editor, path) with Stdin/Stdout/Stderr connected
5. Return editor's exit code

## Changes to config.go

Add `Editor string \`json:"editor"\`` to Config struct.

## Changes to run.go

- Add `case "editor":`
- Add help text

## Acceptance Criteria

## Functionality

- [ ] `tk editor <id>` opens ticket in editor
- [ ] Editor resolves: config.Editor -> $EDITOR -> vi -> nano -> error
- [ ] Waits for editor to exit
- [ ] Returns editor's exit code

## Validation

- [ ] Error if no ID provided
- [ ] Error if ticket doesn't exist
- [ ] Error if no editor found (none of the 4 options available)

## Config

- [ ] `editor` field in .tk.json is optional
- [ ] If set, used before $EDITOR

## Help

- [ ] `tk editor --help` shows usage
- [ ] `tk --help` lists editor command

## Tests

### Mock Editor Setup

Create mock editor script in temp dir at start of parent test, reuse in subtests:

```go
func TestEditorCommand(t *testing.T) {
    // Setup mock editor once
    mockDir := t.TempDir()
    mockEditor := filepath.Join(mockDir, "mock-editor")
    script := `#!/bin/sh
echo "$@" > "` + mockDir + `/invoked.txt"
exit 0
`
    os.WriteFile(mockEditor, []byte(script), 0755)
    
    t.Run("config editor used first", func(t *testing.T) { ... })
    t.Run("EDITOR env fallback", func(t *testing.T) { ... })
}
```

Tests check `invoked.txt` to verify:
- Editor was called
- Correct ticket path was passed as argument

### Using env parameter in Run()

`Run()` already has an `env []string` parameter (currently ignored). Wire it through to `cmdEditor()` so tests can control `$EDITOR`:

```go
// Test config.Editor takes priority over $EDITOR
t.Run("config editor used first", func(t *testing.T) {
    writeConfig(tmpDir, `{"editor": "`+mockEditor+`"}`)
    env := []string{"EDITOR=/should/not/use/this"}
    Run(nil, &stdout, &stderr, args, env)
    // verify mockEditor was called
})

// Test $EDITOR fallback when no config.Editor
t.Run("EDITOR env fallback", func(t *testing.T) {
    // no config.Editor set
    env := []string{"EDITOR=" + mockEditor}
    Run(nil, &stdout, &stderr, args, env)
    // verify mockEditor was called
})
```

### Test Cases

- [ ] editor_test.go covers missing ID error
- [ ] editor_test.go covers ticket not found error
- [ ] editor_test.go covers config.Editor used first (over $EDITOR)
- [ ] editor_test.go covers $EDITOR fallback when no config
- [ ] editor_test.go covers vi fallback when no $EDITOR
- [ ] editor_test.go covers nano fallback when no vi
- [ ] editor_test.go covers no editor found error
- [ ] editor_test.go verifies correct file path passed to editor
