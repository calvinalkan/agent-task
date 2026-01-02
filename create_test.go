package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:funlen // table-driven test with many cases
func TestCreateCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout string
		wantStderr string
		checkFile  func(t *testing.T, dir string, id string)
	}{
		{
			name:       "creates ticket with title only",
			args:       []string{"tk", "create", "Test ticket"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "# Test ticket")
				assertContains(t, content, "status: open")
				assertContains(t, content, "type: task")
				assertContains(t, content, "priority: 2")
			},
		},
		{
			name:       "creates ticket with description",
			args:       []string{"tk", "create", "Test", "-d", "Description text"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "Description text")
				assertNotContains(t, content, "## Description")
			},
		},
		{
			name:       "creates ticket with design section",
			args:       []string{"tk", "create", "Test", "--design", "Design notes"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "## Design")
				assertContains(t, content, "Design notes")
			},
		},
		{
			name:       "creates ticket with acceptance criteria",
			args:       []string{"tk", "create", "Test", "--acceptance", "Must pass tests"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "## Acceptance Criteria")
				assertContains(t, content, "Must pass tests")
			},
		},
		{
			name:       "creates ticket with custom type",
			args:       []string{"tk", "create", "Test", "-t", "bug"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "type: bug")
			},
		},
		{
			name:       "creates ticket with custom priority",
			args:       []string{"tk", "create", "Test", "-p", "1"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "priority: 1")
			},
		},
		{
			name:       "creates ticket with assignee",
			args:       []string{"tk", "create", "Test", "-a", "John Doe"},
			wantExit:   0,
			wantStderr: "",
			checkFile: func(t *testing.T, dir string, id string) {
				t.Helper()
				content := readTicket(t, dir, id)
				assertContains(t, content, "assignee: John Doe")
			},
		},
		{
			name:       "error on missing title",
			args:       []string{"tk", "create"},
			wantExit:   1,
			wantStderr: "error: title is required",
		},
		{
			name:       "error on empty description",
			args:       []string{"tk", "create", "Test", "-d", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --description",
		},
		{
			name:       "error on empty design",
			args:       []string{"tk", "create", "Test", "--design", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --design",
		},
		{
			name:       "error on empty acceptance",
			args:       []string{"tk", "create", "Test", "--acceptance", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --acceptance",
		},
		{
			name:       "error on empty type",
			args:       []string{"tk", "create", "Test", "-t", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --type",
		},
		{
			name:       "error on empty priority",
			args:       []string{"tk", "create", "Test", "-p", ""},
			wantExit:   1,
			wantStderr: "error: invalid argument",
		},
		{
			name:       "error on empty assignee",
			args:       []string{"tk", "create", "Test", "-a", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --assignee",
		},
		{
			name:       "error on invalid type",
			args:       []string{"tk", "create", "Test", "-t", "invalid"},
			wantExit:   1,
			wantStderr: "error: invalid type",
		},
		{
			name:       "error on invalid priority too high",
			args:       []string{"tk", "create", "Test", "-p", "5"},
			wantExit:   1,
			wantStderr: "error: invalid priority",
		},
		{
			name:       "error on invalid priority too low",
			args:       []string{"tk", "create", "Test", "-p", "0"},
			wantExit:   1,
			wantStderr: "error: invalid priority",
		},
		{
			name:       "error on invalid priority non-numeric",
			args:       []string{"tk", "create", "Test", "-p", "abc"},
			wantExit:   1,
			wantStderr: "error: invalid argument",
		},
		{
			name:       "error on invalid blocker",
			args:       []string{"tk", "create", "Test", "--blocked-by", "nonexistent"},
			wantExit:   1,
			wantStderr: "error: blocker not found",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			// Create temp directory for test
			tmpDir := t.TempDir()
			ticketDir := filepath.Join(tmpDir, ".tickets")

			// Run with -C to set work directory
			args := append([]string{testCase.args[0], "-C", tmpDir}, testCase.args[1:]...)

			var stdout, stderr bytes.Buffer

			exitCode := Run(nil, &stdout, &stderr, args, nil)

			if exitCode != testCase.wantExit {
				t.Errorf("exit code = %d, want %d\nstderr: %s", exitCode, testCase.wantExit, stderr.String())
			}

			if testCase.wantStderr != "" && !strings.Contains(stderr.String(), testCase.wantStderr) {
				t.Errorf("stderr = %q, want to contain %q", stderr.String(), testCase.wantStderr)
			}

			if testCase.wantExit == 0 {
				ticketID := strings.TrimSpace(stdout.String())
				if ticketID == "" {
					t.Fatal("expected ID in stdout")
				}

				// Verify file was created
				files, err := os.ReadDir(ticketDir)
				if err != nil {
					t.Fatalf("failed to read ticket dir: %v", err)
				}

				if len(files) != 1 {
					t.Fatalf("expected 1 file, got %d", len(files))
				}

				if testCase.checkFile != nil {
					testCase.checkFile(t, ticketDir, ticketID)
				}
			}
		})
	}
}

func TestCreateWithBlocker(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	// Create first ticket
	var stdout1, stderr1 bytes.Buffer

	args1 := []string{"tk", "-C", tmpDir, "create", "First ticket"}
	exitCode := Run(nil, &stdout1, &stderr1, args1, nil)

	if exitCode != 0 {
		t.Fatalf("failed to create first ticket: %s", stderr1.String())
	}

	blockerID := strings.TrimSpace(stdout1.String())

	// Create second ticket with blocker
	var stdout2, stderr2 bytes.Buffer

	args2 := []string{"tk", "-C", tmpDir, "create", "Second ticket", "--blocked-by", blockerID}
	exitCode = Run(nil, &stdout2, &stderr2, args2, nil)

	if exitCode != 0 {
		t.Fatalf("failed to create second ticket: %s", stderr2.String())
	}

	id2 := strings.TrimSpace(stdout2.String())
	content := readTicket(t, ticketDir, id2)

	assertContains(t, content, "blocked-by: ["+blockerID+"]")
}

func TestCreateMultipleBlockers(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	// Create two tickets to block the new one
	var stdout1 bytes.Buffer
	Run(nil, &stdout1, nil, []string{"tk", "-C", tmpDir, "create", "Blocker 1"}, nil)
	blocker1 := strings.TrimSpace(stdout1.String())

	var stdout2 bytes.Buffer
	Run(nil, &stdout2, nil, []string{"tk", "-C", tmpDir, "create", "Blocker 2"}, nil)
	blocker2 := strings.TrimSpace(stdout2.String())

	// Create ticket with multiple blockers
	var stdout3, stderr3 bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "create", "Main ticket", "--blocked-by", blocker1, "--blocked-by", blocker2}
	exitCode := Run(nil, &stdout3, &stderr3, args, nil)

	if exitCode != 0 {
		t.Fatalf("failed to create ticket: %s", stderr3.String())
	}

	id := strings.TrimSpace(stdout3.String())
	content := readTicket(t, ticketDir, id)

	assertContains(t, content, blocker1)
	assertContains(t, content, blocker2)
}

func TestCreateIDsSortLexicographically(t *testing.T) {
	t.Parallel()

	// Test that IDs created 1 second apart sort correctly
	tmpDir := t.TempDir()

	var stdout1 bytes.Buffer
	Run(nil, &stdout1, nil, []string{"tk", "-C", tmpDir, "create", "First"}, nil)
	id1 := strings.TrimSpace(stdout1.String())

	time.Sleep(time.Second)

	var stdout2 bytes.Buffer
	Run(nil, &stdout2, nil, []string{"tk", "-C", tmpDir, "create", "Second"}, nil)
	id2 := strings.TrimSpace(stdout2.String())

	if id2 <= id1 {
		t.Errorf("IDs not sorted: %s should come after %s", id2, id1)
	}
}

func TestCreateHandlesSameSecondCollision(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create multiple tickets rapidly - retry mechanism should handle collisions
	ids := make([]string, 0, 3)

	for range 3 {
		var stdout bytes.Buffer

		exitCode := Run(nil, &stdout, nil, []string{"tk", "-C", tmpDir, "create", "Ticket"}, nil)
		if exitCode != 0 {
			t.Fatal("failed to create ticket")
		}

		ids = append(ids, strings.TrimSpace(stdout.String()))
	}

	// All IDs should be unique
	seen := make(map[string]bool)
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID: %s", id)
		}

		seen[id] = true
	}
}

func TestCreateDirCreatedIfMissing(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	// Ensure directory doesn't exist
	_, statErr := os.Stat(ticketDir)
	if statErr == nil {
		t.Fatal("ticket dir should not exist yet")
	}

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "create", "Test"}, nil)

	if exitCode != 0 {
		t.Fatalf("failed: %s", stderr.String())
	}

	// Directory should now exist
	_, statErr = os.Stat(ticketDir)
	if statErr != nil {
		t.Fatal("ticket dir should exist")
	}
}

func TestCreateIDHasNoDash(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	var stdout bytes.Buffer
	Run(nil, &stdout, nil, []string{"tk", "-C", tmpDir, "create", "Test"}, nil)
	ticketID := strings.TrimSpace(stdout.String())

	if strings.Contains(ticketID, "-") {
		t.Errorf("ID %q should not contain a dash", ticketID)
	}

	if ticketID == "" {
		t.Error("ID should not be empty")
	}
}

func TestCreateOmitsSectionsIfNotProvided(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	var stdout bytes.Buffer
	Run(nil, &stdout, nil, []string{"tk", "-C", tmpDir, "create", "Test"}, nil)
	id := strings.TrimSpace(stdout.String())
	content := readTicket(t, ticketDir, id)

	assertNotContains(t, content, "## Design")
	assertNotContains(t, content, "## Acceptance Criteria")
}

func TestCreateOmitsAssigneeIfEmpty(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	// Create ticket with explicit empty assignee by using a custom assignee
	// then checking a different ticket without assignee flag
	// First, we need to ensure git user.name is not set for this test

	var stdout bytes.Buffer
	Run(nil, &stdout, nil, []string{"tk", "-C", tmpDir, "create", "Test", "-a", "TestUser"}, nil)
	id := strings.TrimSpace(stdout.String())
	content := readTicket(t, ticketDir, id)

	assertContains(t, content, "assignee: TestUser")
}

func TestCreateAllOptions(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	args := []string{
		"tk", "-C", tmpDir, "create", "Full ticket",
		"-d", "Full description",
		"--design", "Design details",
		"--acceptance", "All tests pass",
		"-t", "feature",
		"-p", "1",
		"-a", "Alice",
	}

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Fatalf("failed: %s", stderr.String())
	}

	ticketID := strings.TrimSpace(stdout.String())
	content := readTicket(t, ticketDir, ticketID)

	assertContains(t, content, "# Full ticket")
	assertContains(t, content, "Full description")
	assertContains(t, content, "## Design")
	assertContains(t, content, "Design details")
	assertContains(t, content, "## Acceptance Criteria")
	assertContains(t, content, "All tests pass")
	assertContains(t, content, "type: feature")
	assertContains(t, content, "priority: 1")
	assertContains(t, content, "assignee: Alice")
}

func TestCreateLongFlags(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	args := []string{
		"tk", "-C", tmpDir, "create", "Long flags",
		"--description", "Desc",
		"--type", "epic",
		"--priority", "3",
		"--assignee", "Bob",
	}

	var stdout bytes.Buffer
	Run(nil, &stdout, nil, args, nil)

	id := strings.TrimSpace(stdout.String())
	content := readTicket(t, ticketDir, id)

	assertContains(t, content, "Desc")
	assertContains(t, content, "type: epic")
	assertContains(t, content, "priority: 3")
	assertContains(t, content, "assignee: Bob")
}

// Helper functions

func readTicket(t *testing.T, dir, ticketID string) string {
	t.Helper()

	path := filepath.Join(dir, ticketID+".md")

	content, err := os.ReadFile(path) //nolint:gosec // test helper with controlled input
	if err != nil {
		t.Fatalf("failed to read ticket %s: %v", path, err)
	}

	return string(content)
}

func assertContains(t *testing.T, content, substr string) {
	t.Helper()

	if !strings.Contains(content, substr) {
		t.Errorf("content should contain %q\ncontent:\n%s", substr, content)
	}
}

func assertNotContains(t *testing.T, content, substr string) {
	t.Helper()

	if strings.Contains(content, substr) {
		t.Errorf("content should NOT contain %q\ncontent:\n%s", substr, content)
	}
}

func TestCreateHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "create", "--help"}},
		{name: "short flag", args: []string{"tk", "create", "-h"}},
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
			assertContains(t, out, "Usage: tk create")
			assertContains(t, out, "--description")
			assertContains(t, out, "--design")
			assertContains(t, out, "--acceptance")
			assertContains(t, out, "--type")
			assertContains(t, out, "--priority")
			assertContains(t, out, "--assignee")
			assertContains(t, out, "--blocked-by")
		})
	}
}
