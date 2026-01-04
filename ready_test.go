package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadyEmptyDir(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

func TestReadyOpenTicket(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	ticketID := strings.TrimSpace(createStdout.String())

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(stdout.String(), ticketID) {
		t.Errorf("stdout = %q, want to contain %q", stdout.String(), ticketID)
	}

	if !strings.Contains(stdout.String(), "[open]") {
		t.Errorf("stdout = %q, want to contain '[open]'", stdout.String())
	}
}

func TestReadyInProgressExcluded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create and start a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	ticketID := strings.TrimSpace(createStdout.String())

	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	// in_progress tickets should NOT be in ready list
	if strings.Contains(stdout.String(), ticketID) {
		t.Errorf("stdout = %q, should not contain in_progress ticket %q", stdout.String(), ticketID)
	}
}

func TestReadyClosedExcluded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create, start, and close a ticket
	var createStdout, discard bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	ticketID := strings.TrimSpace(createStdout.String())

	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)
	Run(nil, &discard, &discard, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if strings.Contains(stdout.String(), ticketID) {
		t.Errorf("stdout = %q, should not contain closed ticket %q", stdout.String(), ticketID)
	}
}

func TestReadyBlockedByOpen(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer
	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "Blocker"}, nil)
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocked"}, nil)

	blockerID := strings.TrimSpace(createStdout1.String())
	blockedID := strings.TrimSpace(createStdout2.String())

	// Block the second by the first
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "block", blockedID, blockerID}, nil)

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	// Blocker should be ready
	if !strings.Contains(stdout.String(), blockerID) {
		t.Errorf("stdout = %q, want to contain blocker %q", stdout.String(), blockerID)
	}

	// Blocked ticket should not be ready
	if strings.Contains(stdout.String(), blockedID) {
		t.Errorf("stdout = %q, should not contain blocked ticket %q", stdout.String(), blockedID)
	}
}

func TestReadyBlockedByInProgress(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer
	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "Blocker"}, nil)
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocked"}, nil)

	blockerID := strings.TrimSpace(createStdout1.String())
	blockedID := strings.TrimSpace(createStdout2.String())

	// Start the blocker
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "start", blockerID}, nil)

	// Block the second by the first
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "block", blockedID, blockerID}, nil)

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	// Blocked ticket should not be ready
	if strings.Contains(stdout.String(), blockedID) {
		t.Errorf("stdout = %q, should not contain blocked ticket %q", stdout.String(), blockedID)
	}
}

func TestReadyBlockedByClosed(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets
	var createStdout1, createStdout2 bytes.Buffer
	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "Blocker"}, nil)

	// Wait to ensure different IDs
	time.Sleep(time.Millisecond * 10)

	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocked"}, nil)

	blockerID := strings.TrimSpace(createStdout1.String())
	blockedID := strings.TrimSpace(createStdout2.String())

	// Block the second by the first
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "block", blockedID, blockerID}, nil)

	// Close the blocker
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "start", blockerID}, nil)
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "close", blockerID}, nil)

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	// Blocked ticket should now be ready
	if !strings.Contains(stdout.String(), blockedID) {
		t.Errorf("stdout = %q, want to contain unblocked ticket %q", stdout.String(), blockedID)
	}

	// Closed blocker should not appear (check for ID followed by space to avoid prefix match)
	if strings.Contains(stdout.String(), blockerID+" ") {
		t.Errorf("stdout = %q, should not contain closed blocker %q", stdout.String(), blockerID)
	}
}

func TestReadyBlockedByNonExistent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := tmpDir + "/.tickets"

	// Create a ticket
	var createStdout bytes.Buffer
	Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)

	ticketID := strings.TrimSpace(createStdout.String())

	// Manually add a non-existent blocker
	content := readTicket(t, ticketDir, ticketID)
	newContent := strings.Replace(content, "blocked-by: []", "blocked-by: [nonexistent123]", 1)

	err := writeTicketContent(t, ticketDir, ticketID, newContent)
	if err != nil {
		t.Fatalf("failed to write ticket: %v", err)
	}

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	// Exit code is 1 due to warning
	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1 (due to warning)", exitCode)
	}

	// Ticket should still be ready
	if !strings.Contains(stdout.String(), ticketID) {
		t.Errorf("stdout = %q, want to contain %q", stdout.String(), ticketID)
	}

	// Should have warning about non-existent blocker
	if !strings.Contains(stderr.String(), "non-existent ticket") {
		t.Errorf("stderr = %q, want to contain 'non-existent ticket'", stderr.String())
	}

	if !strings.Contains(stderr.String(), "nonexistent123") {
		t.Errorf("stderr = %q, want to contain 'nonexistent123'", stderr.String())
	}
}

func TestReadySortByPriority(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create tickets with different priorities (in wrong order)
	var createStdout1, createStdout2, createStdout3 bytes.Buffer

	// Create P3 first
	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "-p", "3", "P3 ticket"}, nil)

	time.Sleep(time.Millisecond * 10)

	// Create P1 second
	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "-p", "1", "P1 ticket"}, nil)

	time.Sleep(time.Millisecond * 10)

	// Create P2 third
	Run(nil, &createStdout3, nil, []string{"tk", "-C", tmpDir, "create", "-p", "2", "P2 ticket"}, nil)

	p3ID := strings.TrimSpace(createStdout1.String())
	p1ID := strings.TrimSpace(createStdout2.String())
	p2ID := strings.TrimSpace(createStdout3.String())

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	output := stdout.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}

	// Check order: P1, P2, P3
	assertContains(t, lines[0], p1ID)
	assertContains(t, lines[1], p2ID)
	assertContains(t, lines[2], p3ID)

	// Verify priority labels
	assertContains(t, lines[0], "[P1]")
	assertContains(t, lines[1], "[P2]")
	assertContains(t, lines[2], "[P3]")
}

func TestReadySamePrioritySortByID(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create tickets with same priority
	var createStdout1, createStdout2 bytes.Buffer

	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "-p", "2", "First ticket"}, nil)

	time.Sleep(time.Millisecond * 10)

	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "-p", "2", "Second ticket"}, nil)

	id1 := strings.TrimSpace(createStdout1.String())
	id2 := strings.TrimSpace(createStdout2.String())

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	output := stdout.String()
	lines := strings.Split(strings.TrimSpace(output), "\n")

	// First created (earlier ID) should come first
	if len(lines) >= 2 {
		assertContains(t, lines[0], id1)
		assertContains(t, lines[1], id2)
	}
}

func TestReadyOutputFormat(t *testing.T) {
	t.Parallel()

	t.Run("output shows priority and status and title", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()

		var createStdout bytes.Buffer
		Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "-p", "1", "My important task"}, nil)

		ticketID := strings.TrimSpace(createStdout.String())

		var stdout, stderr bytes.Buffer

		exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "ready"}, nil)

		if exitCode != 0 {
			t.Errorf("exit code = %d, want 0", exitCode)
		}

		output := stdout.String()

		if !strings.Contains(output, ticketID) {
			t.Errorf("output = %q, want to contain ID %q", output, ticketID)
		}

		if !strings.Contains(output, "[P1]") {
			t.Errorf("output = %q, want to contain '[P1]'", output)
		}

		if !strings.Contains(output, "[open]") {
			t.Errorf("output = %q, want to contain '[open]'", output)
		}

		if !strings.Contains(output, "My important task") {
			t.Errorf("output = %q, want to contain 'My important task'", output)
		}
	})
}

//nolint:dupl // similar test structure is acceptable for different commands
func TestReadyHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "ready", "--help"}},
		{name: "short flag", args: []string{"tk", "ready", "-h"}},
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
			if !strings.Contains(out, "Usage: tk ready") {
				t.Errorf("stdout = %q, want to contain 'Usage: tk ready'", out)
			}
		})
	}
}

func TestReadyInMainHelp(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "--help"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(stdout.String(), "ready") {
		t.Errorf("stdout = %q, want to contain 'ready'", stdout.String())
	}
}

func TestReadyActiveTicketCoverage(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create tickets with various states
	var createStdout1, createStdout2, createStdout3, createStdout4 bytes.Buffer

	Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "Open ticket"}, nil)

	time.Sleep(time.Millisecond * 10)

	Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocker"}, nil)

	time.Sleep(time.Millisecond * 10)

	Run(nil, &createStdout3, nil, []string{"tk", "-C", tmpDir, "create", "Blocked ticket"}, nil)

	time.Sleep(time.Millisecond * 10)

	Run(nil, &createStdout4, nil, []string{"tk", "-C", tmpDir, "create", "To be closed"}, nil)

	openID := strings.TrimSpace(createStdout1.String())
	blockerID := strings.TrimSpace(createStdout2.String())
	blockedID := strings.TrimSpace(createStdout3.String())
	closedID := strings.TrimSpace(createStdout4.String())

	// Block one ticket
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "block", blockedID, blockerID}, nil)

	// Close one ticket
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "start", closedID}, nil)
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "close", closedID}, nil)

	// Get ready list
	var readyStdout bytes.Buffer
	Run(nil, &readyStdout, nil, []string{"tk", "-C", tmpDir, "ready"}, nil)

	readyOutput := readyStdout.String()

	// Get all active (non-closed) tickets
	var lsStdout bytes.Buffer
	Run(nil, &lsStdout, nil, []string{"tk", "-C", tmpDir, "ls"}, nil)

	lsOutput := lsStdout.String()

	// Verify invariants:
	assertContains(t, readyOutput, openID)
	assertContains(t, readyOutput, blockerID)
	assertNotContains(t, readyOutput, blockedID)
	assertNotContains(t, readyOutput, closedID)
	assertContains(t, lsOutput, blockedID)
}

func TestReadyIdempotent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create some tickets
	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "create", "-p", "1", "P1 ticket"}, nil)

	time.Sleep(time.Millisecond * 10)

	Run(nil, io.Discard, io.Discard, []string{"tk", "-C", tmpDir, "create", "-p", "2", "P2 ticket"}, nil)

	// Run ready twice
	var stdout1, stdout2 bytes.Buffer

	Run(nil, &stdout1, nil, []string{"tk", "-C", tmpDir, "ready"}, nil)
	Run(nil, &stdout2, nil, []string{"tk", "-C", tmpDir, "ready"}, nil)

	if stdout1.String() != stdout2.String() {
		t.Errorf("output differs between runs:\nfirst: %q\nsecond: %q", stdout1.String(), stdout2.String())
	}
}
