package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tk/internal/cli"
)

func readTicketClose(t *testing.T, dir, ticketID string) string {
	t.Helper()

	path := filepath.Join(dir, ticketID+".md")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read ticket %s: %v", path, err)
	}

	return string(content)
}

func assertContainsClose(t *testing.T, content, substr string) {
	t.Helper()

	if !strings.Contains(content, substr) {
		t.Errorf("content should contain %q\ncontent:\n%s", substr, content)
	}
}

func writeTicketContentClose(t *testing.T, dir, ticketID, content string) {
	t.Helper()

	path := filepath.Join(dir, ticketID+".md")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("writing ticket: %v", err)
	}
}

func TestCloseCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"tk", "close"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"tk", "close", "nonexistent"},
			wantExit:   1,
			wantStderr: "ticket not found",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()

			// Prepend -C tmpDir to args
			args := append([]string{testCase.args[0], "-C", tmpDir}, testCase.args[1:]...)

			var stdout, stderr bytes.Buffer

			exitCode := cli.Run(nil, &stdout, &stderr, args, nil)

			if exitCode != testCase.wantExit {
				t.Errorf("exit code = %d, want %d", exitCode, testCase.wantExit)
			}

			if testCase.wantStderr != "" && !strings.Contains(stderr.String(), testCase.wantStderr) {
				t.Errorf("stderr = %q, want to contain %q", stderr.String(), testCase.wantStderr)
			}
		})
	}
}

func TestCloseInProgressTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := cli.Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	var discard bytes.Buffer
	// Start the ticket
	exitCode = cli.Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	if exitCode != 0 {
		t.Fatal("failed to start ticket")
	}

	var stdout, stderr bytes.Buffer
	// Close the ticket
	exitCode = cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	if exitCode != 0 {
		t.Fatalf("close failed: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "Closed") {
		t.Errorf("stdout = %q, want to contain 'Closed'", stdout.String())
	}

	// Verify status changed
	content := readTicketClose(t, ticketDir, ticketID)
	assertContainsClose(t, content, "status: closed")
}

func TestCloseAddsTimestamp(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create and start a ticket
	var createStdout bytes.Buffer
	cli.Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	var discard bytes.Buffer
	cli.Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	// Close the ticket
	cli.Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	// Verify closed timestamp is added
	content := readTicketClose(t, ticketDir, ticketID)
	assertContainsClose(t, content, "closed: ")

	// Verify it's in ISO 8601 format (contains T and Z)
	if !strings.Contains(content, "closed: 20") || !strings.Contains(content, "T") {
		t.Errorf("closed timestamp not in expected format, content = %q", content)
	}
}

func TestCloseOpenTicketError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket (starts as open)
	var createStdout bytes.Buffer
	cli.Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	// Try to close without starting
	var stdout, stderr bytes.Buffer

	exitCode := cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "must be started first") {
		t.Errorf("stderr = %q, want to contain 'must be started first'", stderr.String())
	}
}

func TestCloseAlreadyClosedTicketError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create a ticket
	var createStdout bytes.Buffer
	cli.Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	// Manually set to closed
	content := readTicketClose(t, ticketDir, ticketID)
	content = strings.Replace(content, "status: open", "status: closed", 1)

	writeTicketContentClose(t, ticketDir, ticketID, content)

	// Try to close again
	var stdout, stderr bytes.Buffer

	exitCode := cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "already closed") {
		t.Errorf("stderr = %q, want to contain 'already closed'", stderr.String())
	}
}

func TestCloseStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout, stderr bytes.Buffer

	exitCode := cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "close", "nonexistent"}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestCloseHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "close", "--help"}},
		{name: "short flag", args: []string{"tk", "close", "-h"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			exitCode := cli.Run(nil, &stdout, &stderr, testCase.args, nil)

			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}

			out := stdout.String()
			if !strings.Contains(out, "Usage: tk close") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk close'", out)
			}

			if !strings.Contains(out, "closed") {
				t.Errorf("stdout = %q, want to contain 'closed'", out)
			}
		})
	}
}
