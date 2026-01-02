package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

//nolint:dupl // similar test structure is acceptable for different commands
func TestStartCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"tk", "start"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"tk", "start", "nonexistent"},
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

func TestStartOpenTicket(t *testing.T) {
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
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	if exitCode != 0 {
		t.Fatalf("start failed: %s", stderr.String())
	}

	output := stdout.String()

	if !strings.Contains(output, "Started") {
		t.Errorf("stdout = %q, want to contain 'Started'", output)
	}

	// Verify ticket content is printed
	if !strings.Contains(output, "status: in_progress") {
		t.Errorf("stdout = %q, want to contain 'status: in_progress'", output)
	}

	if !strings.Contains(output, "Test ticket") {
		t.Errorf("stdout = %q, want to contain 'Test ticket'", output)
	}

	// Verify status changed in file
	content := readTicket(t, ticketDir, ticketID)
	assertContains(t, content, "status: in_progress")
}

func TestStartAlreadyInProgressTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	// Start the ticket once
	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	// Try to start again
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "ticket is not open") {
		t.Errorf("stderr = %q, want to contain 'ticket is not open'", stderr.String())
	}

	if !strings.Contains(stderr.String(), "in_progress") {
		t.Errorf("stderr = %q, want to contain 'in_progress'", stderr.String())
	}
}

func TestStartClosedTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	ticketID := strings.TrimSpace(createStdout.String())

	// Manually close the ticket by updating the file
	content := readTicket(t, ticketDir, ticketID)
	content = strings.Replace(content, "status: open", "status: closed", 1)

	writeErr := writeTicketContent(t, ticketDir, ticketID, content)
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	// Try to start the closed ticket
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "ticket is not open") {
		t.Errorf("stderr = %q, want to contain 'ticket is not open'", stderr.String())
	}

	if !strings.Contains(stderr.String(), "closed") {
		t.Errorf("stderr = %q, want to contain 'closed'", stderr.String())
	}
}

func TestStartStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "start", "nonexistent"}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func writeTicketContent(t *testing.T, dir, ticketID, content string) error {
	t.Helper()

	path := dir + "/" + ticketID + ".md"

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		return fmt.Errorf("writing ticket: %w", err)
	}

	return nil
}

//nolint:dupl // similar test structure is acceptable for different commands
func TestStartHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "start", "--help"}},
		{name: "short flag", args: []string{"tk", "start", "-h"}},
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
			if !strings.Contains(out, "Usage: tk start") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk start'", out)
			}

			if !strings.Contains(out, "in_progress") {
				t.Errorf("stdout = %q, want to contain 'in_progress'", out)
			}
		})
	}
}
