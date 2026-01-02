package main

import (
	"bytes"
	"strings"
	"testing"
)

//nolint:dupl // test structure similar to close_test.go but tests different command
func TestReopenCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"tk", "reopen"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"tk", "reopen", "nonexistent"},
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

			exitCode := Run(nil, &stdout, &stderr, args, nil)

			if exitCode != testCase.wantExit {
				t.Errorf("exit code = %d, want %d", exitCode, testCase.wantExit)
			}

			if testCase.wantStderr != "" && !strings.Contains(stderr.String(), testCase.wantStderr) {
				t.Errorf("stderr = %q, want to contain %q", stderr.String(), testCase.wantStderr)
			}
		})
	}
}

func TestReopenClosedTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Start the ticket
	var discard bytes.Buffer

	exitCode = Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	if exitCode != 0 {
		t.Fatal("failed to start ticket")
	}

	// Close the ticket
	exitCode = Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	if exitCode != 0 {
		t.Fatal("failed to close ticket")
	}

	// Reopen the ticket
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "reopen", ticketID}, nil)

	if exitCode != 0 {
		t.Fatalf("reopen failed: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "Reopened") {
		t.Errorf("stdout = %q, want to contain 'Reopened'", stdout.String())
	}

	// Verify status changed to open
	content := readTicket(t, ticketDir, ticketID)
	assertContains(t, content, "status: open")
}

func TestReopenRemovesClosedTimestamp(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create, start, and close a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	// Verify closed timestamp exists before reopen
	content := readTicket(t, ticketDir, ticketID)
	if !strings.Contains(content, "closed: ") {
		t.Fatal("closed timestamp should exist before reopen")
	}

	// Reopen the ticket
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "reopen", ticketID}, nil)

	// Verify closed timestamp is removed
	content = readTicket(t, ticketDir, ticketID)
	if strings.Contains(content, "closed: ") {
		t.Errorf("closed timestamp should be removed after reopen, content = %q", content)
	}
}

func TestReopenOpenTicketError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket (starts as open)
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	// Try to reopen an already open ticket
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "reopen", ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "already open") {
		t.Errorf("stderr = %q, want to contain 'already open'", stderr.String())
	}
}

func TestReopenInProgressTicketError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create and start a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	// Try to reopen an in_progress ticket
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "reopen", ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "not closed") {
		t.Errorf("stderr = %q, want to contain 'not closed'", stderr.String())
	}
}

func TestReopenStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "reopen", "nonexistent"}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestReopenFullCycleShowContent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create, start, close, reopen
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "reopen", ticketID}, nil)

	// Verify file content
	content := readTicket(t, ticketDir, ticketID)
	assertContains(t, content, "status: open")
	assertContains(t, content, "id: "+ticketID)
	assertContains(t, content, "# Test ticket")

	// Verify no closed timestamp
	if strings.Contains(content, "closed: ") {
		t.Errorf("closed timestamp should not exist, content = %q", content)
	}
}

//nolint:dupl // test structure similar to close_test.go but tests different command
func TestReopenHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "reopen", "--help"}},
		{name: "short flag", args: []string{"tk", "reopen", "-h"}},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer

			exitCode := Run(nil, &stdout, &stderr, testCase.args, nil)

			if exitCode != 0 {
				t.Errorf("exit code = %d, want 0", exitCode)
			}

			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}

			out := stdout.String()
			if !strings.Contains(out, "Usage: tk reopen") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk reopen'", out)
			}

			if !strings.Contains(out, "open") {
				t.Errorf("stdout = %q, want to contain 'open'", out)
			}
		})
	}
}
