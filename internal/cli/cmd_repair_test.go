package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tk/internal/ticket"
)

func backdateCacheRepair(t *testing.T, ticketDir string) {
	t.Helper()

	cachePath := filepath.Join(ticketDir, ticket.CacheFileName)
	past := time.Now().Add(-10 * time.Second)

	err := os.Chtimes(cachePath, past, past)
	if err != nil {
		t.Fatalf("failed to backdate cache: %v", err)
	}
}

func TestRepairCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID and no --all returns error",
			args:       []string{"tk", "repair"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ticket returns error",
			args:       []string{"tk", "repair", "nonexistent"},
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

func TestRepairStaleBlocker(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Manually add a stale blocker to the ticket file
	ticketPath := filepath.Join(tmpDir, ".tickets", ticketID+".md")

	content, err := os.ReadFile(ticketPath)
	if err != nil {
		t.Fatal("failed to read ticket:", err)
	}

	// Replace blocked-by: [] with blocked-by: [nonexistent]
	newContent := strings.Replace(string(content), "blocked-by: []", "blocked-by: [nonexistent]", 1)

	err = os.WriteFile(ticketPath, []byte(newContent), 0o600)
	if err != nil {
		t.Fatal("failed to write ticket:", err)
	}

	// Run repair
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", ticketID}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0, stderr: %s", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Removed stale blocker: nonexistent") {
		t.Errorf("stdout = %q, want to contain 'Removed stale blocker: nonexistent'", stdout.String())
	}

	if !strings.Contains(stdout.String(), "Repaired "+ticketID) {
		t.Errorf("stdout = %q, want to contain 'Repaired %s'", stdout.String(), ticketID)
	}

	// Verify the blocker was removed
	content, err = os.ReadFile(ticketPath)
	if err != nil {
		t.Fatal("failed to read ticket:", err)
	}

	if !strings.Contains(string(content), "blocked-by: []") {
		t.Errorf("ticket content should have 'blocked-by: []' but has: %s", string(content))
	}
}

func TestRepairDryRun(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Manually add a stale blocker to the ticket file
	ticketPath := filepath.Join(tmpDir, ".tickets", ticketID+".md")

	content, err := os.ReadFile(ticketPath)
	if err != nil {
		t.Fatal("failed to read ticket:", err)
	}

	// Replace blocked-by: [] with blocked-by: [stale123]
	newContent := strings.Replace(string(content), "blocked-by: []", "blocked-by: [stale123]", 1)

	err = os.WriteFile(ticketPath, []byte(newContent), 0o600)
	if err != nil {
		t.Fatal("failed to write ticket:", err)
	}

	// Run repair with --dry-run
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", "--dry-run", ticketID}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0, stderr: %s", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Would remove stale blocker: stale123") {
		t.Errorf("stdout = %q, want to contain 'Would remove stale blocker: stale123'", stdout.String())
	}

	// Verify the blocker was NOT removed (dry-run)
	content, err = os.ReadFile(ticketPath)
	if err != nil {
		t.Fatal("failed to read ticket:", err)
	}

	if !strings.Contains(string(content), "blocked-by: [stale123]") {
		t.Errorf("ticket should still have stale blocker after dry-run, content: %s", string(content))
	}
}

func TestRepairAll(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets to repair.
	var ticketOut1, ticketOut2 bytes.Buffer
	if exitCode := Run(nil, &ticketOut1, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 1"}, nil); exitCode != 0 {
		t.Fatal("failed to create ticket 1")
	}

	if exitCode := Run(nil, &ticketOut2, nil, []string{"tk", "-C", tmpDir, "create", "ticket.Ticket 2"}, nil); exitCode != 0 {
		t.Fatal("failed to create ticket 2")
	}

	ticketID1 := strings.TrimSpace(ticketOut1.String())
	ticketID2 := strings.TrimSpace(ticketOut2.String())

	// Create blockers, block tickets, then delete blockers to create stale references.
	var blockerOut1, blockerOut2 bytes.Buffer
	Run(nil, &blockerOut1, nil, []string{"tk", "-C", tmpDir, "create", "Blocker 1"}, nil)
	Run(nil, &blockerOut2, nil, []string{"tk", "-C", tmpDir, "create", "Blocker 2"}, nil)

	blockerID1 := strings.TrimSpace(blockerOut1.String())
	blockerID2 := strings.TrimSpace(blockerOut2.String())

	Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "block", ticketID1, blockerID1}, nil)
	Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "block", ticketID2, blockerID2}, nil)

	// Backdate cache so directory changes are detected.
	backdateCacheRepair(t, filepath.Join(tmpDir, ".tickets"))

	// External deletion changes directory mtime and triggers reconcile.
	_ = os.Remove(filepath.Join(tmpDir, ".tickets", blockerID1+".md"))
	_ = os.Remove(filepath.Join(tmpDir, ".tickets", blockerID2+".md"))

	// Run repair --all
	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", "--all"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0, stderr: %s", exitCode, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "Removed stale blocker: "+blockerID1) {
		t.Errorf("stdout should contain 'Removed stale blocker: %s', got: %s", blockerID1, output)
	}

	if !strings.Contains(output, "Removed stale blocker: "+blockerID2) {
		t.Errorf("stdout should contain 'Removed stale blocker: %s', got: %s", blockerID2, output)
	}

	if !strings.Contains(output, "Repaired "+ticketID1) {
		t.Errorf("stdout should contain 'Repaired %s', got: %s", ticketID1, output)
	}

	if !strings.Contains(output, "Repaired "+ticketID2) {
		t.Errorf("stdout should contain 'Repaired %s', got: %s", ticketID2, output)
	}
}

func TestRepairNothingToRepair(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket with no blockers
	var createStdout bytes.Buffer

	exitCode := Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Clean ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Run repair
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", ticketID}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0, stderr: %s", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Nothing to repair") {
		t.Errorf("stdout = %q, want to contain 'Nothing to repair'", stdout.String())
	}
}

func TestRepairAllNothingToRepair(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket with no blockers
	var createOut bytes.Buffer

	exitCode := Run(nil, &createOut, nil, []string{"tk", "-C", tmpDir, "create", "Clean ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	// Run repair --all
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", "--all"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0, stderr: %s", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), "Nothing to repair") {
		t.Errorf("stdout = %q, want to contain 'Nothing to repair'", stdout.String())
	}
}

func TestRepairHelp(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", "--help"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(stdout.String(), "Usage: tk repair") {
		t.Errorf("stdout = %q, want to contain 'Usage: tk repair'", stdout.String())
	}

	if !strings.Contains(stdout.String(), "--dry-run") {
		t.Errorf("stdout = %q, want to contain '--dry-run'", stdout.String())
	}

	if !strings.Contains(stdout.String(), "--all") {
		t.Errorf("stdout = %q, want to contain '--all'", stdout.String())
	}
}

func TestRepairIdempotent(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket
	var createStdout bytes.Buffer

	exitCode := Run(nil, &createStdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create ticket")
	}

	ticketID := strings.TrimSpace(createStdout.String())

	// Manually add a stale blocker
	ticketPath := filepath.Join(tmpDir, ".tickets", ticketID+".md")
	content, _ := os.ReadFile(ticketPath)
	newContent := strings.Replace(string(content), "blocked-by: []", "blocked-by: [stale]", 1)
	_ = os.WriteFile(ticketPath, []byte(newContent), 0o600)

	// Run repair twice
	var stdout1, stderr1 bytes.Buffer

	exitCode = Run(nil, &stdout1, &stderr1, []string{"tk", "-C", tmpDir, "repair", ticketID}, nil)

	if exitCode != 0 {
		t.Errorf("first repair: exit code = %d, want 0", exitCode)
	}

	var stdout2, stderr2 bytes.Buffer

	exitCode = Run(nil, &stdout2, &stderr2, []string{"tk", "-C", tmpDir, "repair", ticketID}, nil)

	if exitCode != 0 {
		t.Errorf("second repair: exit code = %d, want 0", exitCode)
	}

	// Second run should say nothing to repair
	if !strings.Contains(stdout2.String(), "Nothing to repair") {
		t.Errorf("second repair stdout = %q, want to contain 'Nothing to repair'", stdout2.String())
	}
}

func TestRepairWithValidBlocker(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two tickets
	var createStdout1 bytes.Buffer

	exitCode := Run(nil, &createStdout1, nil, []string{"tk", "-C", tmpDir, "create", "Blocker ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create blocker ticket")
	}

	blockerID := strings.TrimSpace(createStdout1.String())

	var createStdout2 bytes.Buffer

	exitCode = Run(nil, &createStdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocked ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("failed to create blocked ticket")
	}

	blockedID := strings.TrimSpace(createStdout2.String())

	// Block the second ticket by the first
	var blockOut bytes.Buffer

	exitCode = Run(nil, &blockOut, nil, []string{"tk", "-C", tmpDir, "block", blockedID, blockerID}, nil)
	if exitCode != 0 {
		t.Fatal("failed to block ticket")
	}

	// Run repair - should not remove valid blocker
	var stdout, stderr bytes.Buffer

	exitCode = Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "repair", blockedID}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(stdout.String(), "Nothing to repair") {
		t.Errorf("stdout = %q, want to contain 'Nothing to repair'", stdout.String())
	}

	// Verify the blocker is still there
	ticketPath := filepath.Join(tmpDir, ".tickets", blockedID+".md")
	content, _ := os.ReadFile(ticketPath)

	if !strings.Contains(string(content), "blocked-by: ["+blockerID+"]") {
		t.Errorf("valid blocker should still be present, content: %s", string(content))
	}
}

func TestRepairMainHelpShowsRepair(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout bytes.Buffer

	exitCode := Run(nil, &stdout, nil, []string{"tk", "-C", tmpDir, "--help"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	if !strings.Contains(stdout.String(), "repair") {
		t.Errorf("main help should list repair command, got: %s", stdout.String())
	}
}
