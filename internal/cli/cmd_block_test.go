package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tk/internal/cli"
)

func readTicketBlock(t *testing.T, dir, ticketID string) string {
	t.Helper()

	path := filepath.Join(dir, ticketID+".md")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read ticket %s: %v", path, err)
	}

	return string(content)
}

func assertContainsBlock(t *testing.T, content, substr string) {
	t.Helper()

	if !strings.Contains(content, substr) {
		t.Errorf("content should contain %q\ncontent:\n%s", substr, content)
	}
}

func TestBlockCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"tk", "block"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "missing blocker ID returns error",
			args:       []string{"tk", "block", "someid"},
			wantExit:   1,
			wantStderr: "blocker ID is required",
		},
		{
			name:       "nonexistent ticket returns error",
			args:       []string{"tk", "block", "nonexistent", "blocker"},
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

func TestBlockNonexistentBlocker(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := cli.Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Try to block by nonexistent blocker
	var stdout, stderr bytes.Buffer

	exitCode = cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "block", ticketID, "nonexistent"}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "ticket not found") {
		t.Errorf("stderr = %q, want to contain 'ticket not found'", stderr.String())
	}
}

func TestBlockSelf(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := cli.Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Try to block ticket by itself
	var stdout, stderr bytes.Buffer

	exitCode = cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "block", ticketID, ticketID}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "cannot block itself") {
		t.Errorf("stderr = %q, want to contain 'cannot block itself'", stderr.String())
	}
}

func TestBlockTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer

	cli.Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	cli.Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())

	// Block ticket 1 by ticket 2
	var stdout, stderr bytes.Buffer

	exitCode := cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)

	if exitCode != 0 {
		t.Fatalf("block failed: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "Blocked") {
		t.Errorf("stdout = %q, want to contain 'Blocked'", stdout.String())
	}

	if !strings.Contains(stdout.String(), ticketID1) {
		t.Errorf("stdout = %q, want to contain %q", stdout.String(), ticketID1)
	}

	if !strings.Contains(stdout.String(), ticketID2) {
		t.Errorf("stdout = %q, want to contain %q", stdout.String(), ticketID2)
	}

	// Verify blocked-by list updated
	content := readTicketBlock(t, ticketDir, ticketID1)
	assertContainsBlock(t, content, "blocked-by: ["+ticketID2+"]")
}

func TestBlockAlreadyBlocked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer

	cli.Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	cli.Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())

	// Block once
	var discard bytes.Buffer
	cli.Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)

	// Try to block again
	var stdout, stderr bytes.Buffer

	exitCode := cli.Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "already blocked by") {
		t.Errorf("stderr = %q, want to contain 'already blocked by'", stderr.String())
	}
}

func TestBlockMultipleBlockers(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create three tickets
	var createStdout1, createStdout2, createStdout3 bytes.Buffer

	cli.Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	cli.Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)
	cli.Run(nil, &createStdout3, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 3"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())
	ticketID3 := strings.TrimSpace(createStdout3.String())

	// Block ticket 1 by ticket 2 and ticket 3
	var discard bytes.Buffer
	cli.Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)
	cli.Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID3}, nil)

	// Verify both blockers in list
	content := readTicketBlock(t, ticketDir, ticketID1)
	assertContainsBlock(t, content, "blocked-by: ["+ticketID2+", "+ticketID3+"]")
}

func TestBlockHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "block", "--help"}},
		{name: "short flag", args: []string{"tk", "block", "-h"}},
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
			if !strings.Contains(out, "Usage: tk block") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk block'", out)
			}
		})
	}
}
