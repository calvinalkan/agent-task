package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnblockCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"tk", "unblock"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "missing blocker ID returns error",
			args:       []string{"tk", "unblock", "someid"},
			wantExit:   1,
			wantStderr: "blocker ID is required",
		},
		{
			name:       "nonexistent ticket returns error",
			args:       []string{"tk", "unblock", "nonexistent", "blocker"},
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

func TestUnblockNotBlocked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer

	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())

	// Try to unblock when not blocked
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "unblock", ticketID1, ticketID2}, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	if !strings.Contains(stderr.String(), "not blocked by") {
		t.Errorf("stderr = %q, want to contain 'not blocked by'", stderr.String())
	}
}

func TestUnblockTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer

	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())

	// Block ticket 1 by ticket 2
	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)

	// Verify blocked
	content := readTicket(t, ticketDir, ticketID1)
	assertContains(t, content, "blocked-by: ["+ticketID2+"]")

	// Unblock ticket 1 from ticket 2
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "unblock", ticketID1, ticketID2}, nil)

	if exitCode != 0 {
		t.Fatalf("unblock failed: %s", stderr.String())
	}

	if !strings.Contains(stdout.String(), "Unblocked") {
		t.Errorf("stdout = %q, want to contain 'Unblocked'", stdout.String())
	}

	if !strings.Contains(stdout.String(), ticketID1) {
		t.Errorf("stdout = %q, want to contain %q", stdout.String(), ticketID1)
	}

	if !strings.Contains(stdout.String(), ticketID2) {
		t.Errorf("stdout = %q, want to contain %q", stdout.String(), ticketID2)
	}

	// Verify blocked-by list updated
	content = readTicket(t, ticketDir, ticketID1)
	assertContains(t, content, "blocked-by: []")
}

func TestUnblockOnlyRemovesSpecificBlocker(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create three tickets
	var createStdout1, createStdout2, createStdout3 bytes.Buffer

	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)
	Run(nil, &createStdout3, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 3"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())
	ticketID3 := strings.TrimSpace(createStdout3.String())

	// Block ticket 1 by ticket 2 and ticket 3
	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID3}, nil)

	// Unblock only ticket 2
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "unblock", ticketID1, ticketID2}, nil)

	// Verify only ticket 3 remains
	content := readTicket(t, ticketDir, ticketID1)
	assertContains(t, content, "blocked-by: ["+ticketID3+"]")
}

func TestBlockThenUnblockReturnsToOriginalState(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer

	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil)
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil)

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())

	// Get original content
	originalContent := readTicket(t, ticketDir, ticketID1)
	assertContains(t, originalContent, "blocked-by: []")

	// Block then unblock
	var discard bytes.Buffer
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "unblock", ticketID1, ticketID2}, nil)

	// Verify returned to original state
	finalContent := readTicket(t, ticketDir, ticketID1)
	assertContains(t, finalContent, "blocked-by: []")
}

func TestUnblockHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "unblock", "--help"}},
		{name: "short flag", args: []string{"tk", "unblock", "-h"}},
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
			if !strings.Contains(out, "Usage: tk unblock") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk unblock'", out)
			}
		})
	}
}

func TestCreateBlockUnblockWorkflow(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer

	exitCode := Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "Feature ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket 1")
	}

	exitCode = Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocker ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket 2")
	}

	ticketID1 := strings.TrimSpace(createStdout1.String())
	ticketID2 := strings.TrimSpace(createStdout2.String())

	// Initial state: no blockers
	content := readTicket(t, ticketDir, ticketID1)
	assertContains(t, content, "blocked-by: []")

	// Block
	var blockStdout, blockStderr bytes.Buffer

	exitCode = Run(nil, &blockStdout, &blockStderr, []string{"tk", "-C", tmpDir, "block", ticketID1, ticketID2}, nil)

	if exitCode != 0 {
		t.Fatalf("block failed: %s", blockStderr.String())
	}

	// Verify blocked
	content = readTicket(t, ticketDir, ticketID1)
	assertContains(t, content, "blocked-by: ["+ticketID2+"]")

	// Unblock
	var unblockStdout, unblockStderr bytes.Buffer

	exitCode = Run(nil, &unblockStdout, &unblockStderr, []string{"tk", "-C", tmpDir, "unblock", ticketID1, ticketID2}, nil)

	if exitCode != 0 {
		t.Fatalf("unblock failed: %s", unblockStderr.String())
	}

	// Verify unblocked
	content = readTicket(t, ticketDir, ticketID1)
	assertContains(t, content, "blocked-by: []")
}
