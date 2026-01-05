---
schema_version: 1
id: d5dwga0
status: open
blocked-by: [d5dcsc8]
created: 2026-01-05T14:18:16Z
type: task
priority: 2
assignee: Calvin Alkan
---
# Add TestRunner helper for cleaner e2e tests

Add a TestRunner helper in internal/cli/testing.go to reduce boilerplate in CLI tests.

## Design

## Design

```go
// internal/cli/testing.go
type TestRunner struct {
    Dir string
    Env map[string]string
}

func NewTestRunner(t *testing.T) *TestRunner {
    return &TestRunner{
        Dir: t.TempDir(),
        Env: map[string]string{},
    }
}

func (r *TestRunner) Run(args ...string) (stdout, stderr string, exitCode int) {
    var outBuf, errBuf bytes.Buffer
    fullArgs := append([]string{"tk", "-C", r.Dir}, args...)
    exitCode = Run(nil, &outBuf, &errBuf, fullArgs, r.Env)
    return outBuf.String(), errBuf.String(), exitCode
}

func (r *TestRunner) MustRun(t *testing.T, args ...string) string {
    t.Helper()
    stdout, stderr, code := r.Run(args...)
    if code != 0 {
        t.Fatalf("command failed: %v\nstderr: %s", args, stderr)
    }
    return strings.TrimSpace(stdout)
}
```

## Usage
```go
func TestCreateMultiple(t *testing.T) {
    r := NewTestRunner(t)
    
    var ids []string
    for range 3 {
        id := r.MustRun(t, "create", "Ticket")
        ids = append(ids, id)
    }
}
```

## Acceptance Criteria

- [ ] TestRunner struct with Dir and Env fields
- [ ] NewTestRunner(t) creates runner with t.TempDir()
- [ ] Run() returns stdout, stderr, exitCode
- [ ] MustRun() fails test on non-zero exit
- [ ] At least one existing test refactored to use TestRunner
