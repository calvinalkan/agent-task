package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI provides a clean interface for running CLI commands in tests.
// It manages a temp directory and environment variables.
type CLI struct {
	t   *testing.T
	Dir string
	Env map[string]string
}

// NewCLI creates a new test CLI with a temp directory.
func NewCLI(t *testing.T) *CLI {
	t.Helper()

	return &CLI{
		t:   t,
		Dir: t.TempDir(),
		Env: map[string]string{},
	}
}

// Run executes the CLI with the given args and returns stdout, stderr, and exit code.
// Args should not include "tk" or "--cwd" - those are added automatically.
func (r *CLI) Run(args ...string) (string, string, int) {
	var outBuf, errBuf bytes.Buffer

	fullArgs := append([]string{"tk", "--cwd", r.Dir}, args...)
	code := Run(nil, &outBuf, &errBuf, fullArgs, r.Env, nil)

	return outBuf.String(), errBuf.String(), code
}

// RunWithInput executes the CLI with stdin and returns stdout, stderr, and exit code.
// stdin must be a string or io.Reader; panics otherwise.
func (r *CLI) RunWithInput(stdin any, args ...string) (string, string, int) {
	var inReader io.Reader
	switch v := stdin.(type) {
	case string:
		inReader = strings.NewReader(v)
	case io.Reader:
		inReader = v
	default:
		panic(fmt.Sprintf("stdin must be string or io.Reader, got %T", stdin))
	}

	var outBuf, errBuf bytes.Buffer

	fullArgs := append([]string{"tk", "--cwd", r.Dir}, args...)
	code := Run(inReader, &outBuf, &errBuf, fullArgs, r.Env, nil)

	return outBuf.String(), errBuf.String(), code
}

// MustRun executes the CLI and fails the test if the command returns non-zero.
// Returns trimmed stdout on success.
func (r *CLI) MustRun(args ...string) string {
	r.t.Helper()

	stdout, stderr, code := r.Run(args...)
	if code != 0 {
		r.t.Fatalf("command %v failed with exit code %d\nstderr: %s", args, code, stderr)
	}

	return strings.TrimSpace(stdout)
}

// MustFail executes the CLI and fails the test if the command succeeds.
// Also fails if stdout is not empty. Returns trimmed stderr.
func (r *CLI) MustFail(args ...string) string {
	r.t.Helper()

	stdout, stderr, code := r.Run(args...)
	if code == 0 {
		r.t.Fatalf("command %v should have failed but succeeded\nstdout: %s", args, stdout)
	}

	if stdout != "" {
		r.t.Fatalf("command %v failed but stdout should be empty\nstdout: %s", args, stdout)
	}

	return strings.TrimSpace(stderr)
}

// TicketDir returns the path to the .tickets directory.
func (r *CLI) TicketDir() string {
	return filepath.Join(r.Dir, ".tickets")
}

// ReadTicket reads and returns the content of a ticket file.
func (r *CLI) ReadTicket(ticketID string) string {
	r.t.Helper()

	path := filepath.Join(r.TicketDir(), ticketID+".md")

	content, err := os.ReadFile(path)
	if err != nil {
		r.t.Fatalf("failed to read ticket %s: %v", ticketID, err)
	}

	return string(content)
}

// WriteTicket writes content to a ticket file.
func (r *CLI) WriteTicket(ticketID, content string) {
	r.t.Helper()

	path := filepath.Join(r.TicketDir(), ticketID+".md")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		r.t.Fatalf("failed to write ticket %s: %v", ticketID, err)
	}
}

// AssertContains fails the test if content doesn't contain substr.
func AssertContains(t *testing.T, content, substr string) {
	t.Helper()

	if !strings.Contains(content, substr) {
		t.Errorf("content should contain %q\ncontent:\n%s", substr, content)
	}
}

// AssertNotContains fails the test if content contains substr.
func AssertNotContains(t *testing.T, content, substr string) {
	t.Helper()

	if strings.Contains(content, substr) {
		t.Errorf("content should NOT contain %q\ncontent:\n%s", substr, content)
	}
}
