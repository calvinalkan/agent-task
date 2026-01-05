package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestShowCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"tk", "show"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"tk", "show", "nonexistent"},
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

func TestShowCreatedTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket with specific content
	var createStdout bytes.Buffer

	exitCode := Run(nil, &createStdout, nil, []string{
		"tk", "-C", tmpDir, "create", "Test ticket.Ticket Title",
		"-d", "This is the description",
		"--design", "Design notes here",
		"--acceptance", "AC here",
	}, nil)

	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Show the ticket
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "show", ticketID}, nil)

	if exitCode != 0 {
		t.Fatalf("show failed: %s", stderr.String())
	}

	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}

	content := stdout.String()

	// Verify content contains expected parts
	assertContains(t, content, "id: "+ticketID)
	assertContains(t, content, "status: open")
	assertContains(t, content, "# Test ticket.Ticket Title")
	assertContains(t, content, "This is the description")
	assertContains(t, content, "## Design")
	assertContains(t, content, "Design notes here")
	assertContains(t, content, "## Acceptance Criteria")
	assertContains(t, content, "AC here")
}

func TestShowOnlyReadsFromTicketDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a file outside ticket dir with a fake ID name
	fakeID := "fake-id"

	writeErr := writeTicketContent(t, tmpDir, fakeID, "fake content")
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	// Try to show it - should fail because it's not in .tickets/
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "show", fakeID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "ticket not found") {
		t.Errorf("stderr = %q, want to contain 'ticket not found'", stderr.String())
	}
}

func TestShowStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "show", "nonexistent"}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestShowHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "show", "--help"}},
		{name: "short flag", args: []string{"tk", "show", "-h"}},
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
			if !strings.Contains(out, "Usage: tk show") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk show'", out)
			}

			if !strings.Contains(out, "contents") {
				t.Errorf("stdout = %q, want to contain 'contents'", out)
			}
		})
	}
}
