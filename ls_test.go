package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:funlen,gocognit,cyclop // table-driven test with many cases
func TestLsCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		setup      func(t *testing.T, ticketDir string)
		args       []string
		wantExit   int
		wantStdout []string // substrings to find in stdout
		wantStderr []string // substrings to find in stderr
		notStdout  []string // substrings that should NOT be in stdout
	}{
		{
			name:       "no tickets empty output",
			setup:      nil,
			args:       []string{"tk", "ls"},
			wantExit:   0,
			wantStdout: nil,
			wantStderr: nil,
		},
		{
			name: "lists all tickets",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", "open", "First ticket", nil)
				createTestTicket(t, ticketDir, "test-002", "closed", "Second ticket", nil)
			},
			args:       []string{"tk", "ls"},
			wantExit:   0,
			wantStdout: []string{"test-001", "test-002", "[open]", "[closed]", "First ticket", "Second ticket"},
		},
		{
			name: "filter by status open",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", "open", "Open ticket", nil)
				createTestTicket(t, ticketDir, "test-002", "closed", "Closed ticket", nil)
			},
			args:       []string{"tk", "ls", "--status=open"},
			wantExit:   0,
			wantStdout: []string{"test-001", "[open]"},
			notStdout:  []string{"test-002"},
		},
		{
			name: "filter by status closed",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", "open", "Open ticket", nil)
				createTestTicket(t, ticketDir, "test-002", "closed", "Closed ticket", nil)
			},
			args:       []string{"tk", "ls", "--status=closed"},
			wantExit:   0,
			wantStdout: []string{"test-002", "[closed]"},
			notStdout:  []string{"test-001"},
		},
		{
			name: "filter by status in_progress",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", "open", "Open ticket", nil)
				createTestTicket(t, ticketDir, "test-002", "in_progress", "In progress ticket", nil)
			},
			args:       []string{"tk", "ls", "--status=in_progress"},
			wantExit:   0,
			wantStdout: []string{"test-002", "[in_progress]"},
			notStdout:  []string{"test-001"},
		},
		{
			name:       "invalid status error",
			args:       []string{"tk", "ls", "--status=invalid"},
			wantExit:   1,
			wantStderr: []string{"invalid status"},
		},
		{
			name:       "empty status error",
			args:       []string{"tk", "ls", "--status="},
			wantExit:   1,
			wantStderr: []string{"invalid status", "empty"},
		},
		{
			name: "shows blockers in output",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "blocker-001", "open", "Blocker ticket", nil)
				createTestTicket(t, ticketDir, "test-002", "open", "Main ticket", []string{"blocker-001"})
			},
			args:       []string{"tk", "ls"},
			wantExit:   0,
			wantStdout: []string{"test-002", "<- blocked-by: [blocker-001]"},
		},
		{
			name: "multiple blockers in output",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "blocker-001", "open", "Blocker 1", nil)
				createTestTicket(t, ticketDir, "blocker-002", "open", "Blocker 2", nil)
				createTestTicket(t, ticketDir, "test-003", "open", "Main", []string{"blocker-001", "blocker-002"})
			},
			args:       []string{"tk", "ls"},
			wantExit:   0,
			wantStdout: []string{"<- blocked-by: [blocker-001, blocker-002]"},
		},
		{
			name: "no blockers suffix when empty",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", "open", "No blockers", nil)
			},
			args:      []string{"tk", "ls"},
			wantExit:  0,
			notStdout: []string{"<-"},
		},
		{
			name: "sorted by ID oldest first",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				// Create in reverse order to verify sorting
				createTestTicket(t, ticketDir, "z-999", "open", "Last", nil)
				createTestTicket(t, ticketDir, "a-001", "open", "First", nil)
				createTestTicket(t, ticketDir, "m-500", "open", "Middle", nil)
			},
			args:     []string{"tk", "ls"},
			wantExit: 0,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			ticketDir := filepath.Join(tmpDir, ".tickets")

			if testCase.setup != nil {
				// Create ticket dir for setup
				err := os.MkdirAll(ticketDir, 0o750)
				if err != nil {
					t.Fatal(err)
				}

				testCase.setup(t, ticketDir)
			}

			args := append([]string{testCase.args[0], "-C", tmpDir}, testCase.args[1:]...)

			var stdout, stderr bytes.Buffer

			exitCode := Run(nil, &stdout, &stderr, args, nil)

			if exitCode != testCase.wantExit {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
					exitCode, testCase.wantExit, stdout.String(), stderr.String())
			}

			for _, want := range testCase.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout should contain %q\nstdout: %s", want, stdout.String())
				}
			}

			for _, want := range testCase.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr should contain %q\nstderr: %s", want, stderr.String())
				}
			}

			for _, notWant := range testCase.notStdout {
				if strings.Contains(stdout.String(), notWant) {
					t.Errorf("stdout should NOT contain %q\nstdout: %s", notWant, stdout.String())
				}
			}
		})
	}
}

func TestLsOutputOrder(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create tickets with IDs that sort lexicographically
	createTestTicket(t, ticketDir, "aaa-001", "open", "First", nil)
	createTestTicket(t, ticketDir, "bbb-002", "open", "Second", nil)
	createTestTicket(t, ticketDir, "ccc-003", "open", "Third", nil)

	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}

	// Verify order.
	if !strings.HasPrefix(lines[0], "aaa-001") {
		t.Errorf("first line should start with aaa-001, got: %s", lines[0])
	}

	if !strings.HasPrefix(lines[1], "bbb-002") {
		t.Errorf("second line should start with bbb-002, got: %s", lines[1])
	}

	if !strings.HasPrefix(lines[2], "ccc-003") {
		t.Errorf("third line should start with ccc-003, got: %s", lines[2])
	}
}

//nolint:funlen // table-driven test with many validation cases
func TestLsInvalidTicketFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		content    string
		wantStderr string
	}{
		{
			name: "missing id",
			content: "---\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: id",
		},
		{
			name: "missing status",
			content: "---\nid: test-001\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: status",
		},
		{
			name: "missing type",
			content: "---\nid: test-001\nstatus: open\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: type",
		},
		{
			name: "missing priority",
			content: "---\nid: test-001\nstatus: open\ntype: task\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: priority",
		},
		{
			name:       "missing created",
			content:    "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n---\n# Title\n",
			wantStderr: "missing required field: created",
		},
		{
			name: "missing title",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n",
			wantStderr: "no title found",
		},
		{
			name: "invalid status",
			content: "---\nid: test-001\nstatus: pending\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: status",
		},
		{
			name: "invalid type",
			content: "---\nid: test-001\nstatus: open\ntype: story\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: type",
		},
		{
			name: "priority out of range high",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 5\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: priority",
		},
		{
			name: "priority out of range low",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 0\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: priority",
		},
		{
			name: "invalid created timestamp",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04\n---\n# Title\n",
			wantStderr: "invalid field value: created",
		},
		{
			name: "closed without timestamp",
			content: "---\nid: test-001\nstatus: closed\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "closed ticket missing closed timestamp",
		},
		{
			name: "closed timestamp on open ticket",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nclosed: 2026-01-04T01:00:00Z\n---\n# Title\n",
			wantStderr: "closed timestamp on non-closed ticket",
		},
		{
			name:       "no frontmatter",
			content:    "# Just a title\n\nSome content\n",
			wantStderr: "no frontmatter found",
		},
		{
			name:       "unclosed frontmatter",
			content:    "---\nid: test-001\nstatus: open\n# Title\n",
			wantStderr: "unclosed frontmatter",
		},
		{
			name: "empty id",
			content: "---\nid: \nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: id (empty)",
		},
		{
			name: "empty status",
			content: "---\nid: test-001\nstatus: \ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: status (empty)",
		},
		{
			name: "empty assignee if present",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nassignee: \n---\n# Title\n",
			wantStderr: "invalid field value: assignee (empty)",
		},
		{
			name: "blocked-by missing brackets",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nblocked-by: abc-123\n---\n# Title\n",
			wantStderr: "invalid field value: blocked-by (missing brackets)",
		},
		{
			name:       "frontmatter exceeds line limit",
			content:    "---\n" + strings.Repeat("field: value\n", 110),
			wantStderr: "frontmatter exceeds maximum line limit",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			ticketDir := filepath.Join(tmpDir, ".tickets")

			err := os.MkdirAll(ticketDir, 0o750)
			if err != nil {
				t.Fatal(err)
			}

			// Write invalid ticket file
			ticketPath := filepath.Join(ticketDir, "test-001.md")

			err = os.WriteFile(ticketPath, []byte(testCase.content), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer

			args := []string{"tk", "-C", tmpDir, "ls"}
			exitCode := Run(nil, &stdout, &stderr, args, nil)

			if exitCode != 1 {
				t.Errorf("exit code = %d, want 1\nstderr: %s", exitCode, stderr.String())
			}

			if !strings.Contains(stderr.String(), testCase.wantStderr) {
				t.Errorf("stderr should contain %q\nstderr: %s", testCase.wantStderr, stderr.String())
			}
		})
	}
}

func TestLsMixedValidInvalid(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create one valid ticket
	createTestTicket(t, ticketDir, "valid-001", "open", "Valid ticket", nil)

	// Create one invalid ticket (missing type)
	invalidContent := "---\nid: invalid-002\nstatus: open\npriority: 2\ncreated: 2026-01-04T00:00:00Z\n---\n# Invalid\n"

	err = os.WriteFile(filepath.Join(ticketDir, "invalid-002.md"), []byte(invalidContent), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	// Should exit 1 due to invalid ticket
	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1", exitCode)
	}

	// Should show valid ticket in stdout
	if !strings.Contains(stdout.String(), "valid-001") {
		t.Errorf("stdout should contain valid-001\nstdout: %s", stdout.String())
	}

	// Should show warning for invalid ticket in stderr
	if !strings.Contains(stderr.String(), "invalid-002") {
		t.Errorf("stderr should contain invalid-002\nstderr: %s", stderr.String())
	}
}

func TestLsTicketDirNotExist(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	// Don't create .tickets directory

	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	// Should succeed with empty output
	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
	}

	if stdout.String() != "" {
		t.Errorf("stdout should be empty, got: %s", stdout.String())
	}
}

func TestLsHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"tk", "ls", "--help"}},
		{name: "short flag", args: []string{"tk", "ls", "-h"}},
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
			assertContains(t, out, "Usage: tk ls")
			assertContains(t, out, "--status")
		})
	}
}

func TestLsIgnoresNonMdFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create a valid ticket
	createTestTicket(t, ticketDir, "test-001", "open", "Valid ticket", nil)

	// Create non-.md files
	err = os.WriteFile(filepath.Join(ticketDir, "notes.txt"), []byte("some notes"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(ticketDir, ".hidden"), []byte("hidden file"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
	}

	// Should only show the .md ticket
	if !strings.Contains(stdout.String(), "test-001") {
		t.Errorf("stdout should contain test-001")
	}

	if strings.Contains(stdout.String(), "notes") {
		t.Errorf("stdout should not contain notes.txt content")
	}
}

func TestLsIgnoresSubdirectories(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")
	subDir := filepath.Join(ticketDir, "archive")

	err := os.MkdirAll(subDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create a valid ticket in main dir
	createTestTicket(t, ticketDir, "test-001", "open", "Valid ticket", nil)

	// Create a ticket in subdirectory (should be ignored)
	createTestTicket(t, subDir, "archived-001", "closed", "Archived ticket", nil)

	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
	}

	if !strings.Contains(stdout.String(), "test-001") {
		t.Errorf("stdout should contain test-001")
	}

	if strings.Contains(stdout.String(), "archived-001") {
		t.Errorf("stdout should NOT contain archived-001")
	}
}

func TestLsValidBlockedByFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		blockedBy string
		wantErr   bool
	}{
		{name: "empty brackets", blockedBy: "[]", wantErr: false},
		{name: "single blocker", blockedBy: "[abc-123]", wantErr: false},
		{name: "multiple blockers", blockedBy: "[abc-123, def-456]", wantErr: false},
		{name: "missing brackets", blockedBy: "abc-123", wantErr: true},
		{name: "missing open bracket", blockedBy: "abc-123]", wantErr: true},
		{name: "missing close bracket", blockedBy: "[abc-123", wantErr: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			ticketDir := filepath.Join(tmpDir, ".tickets")

			err := os.MkdirAll(ticketDir, 0o750)
			if err != nil {
				t.Fatal(err)
			}

			content := "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nblocked-by: " + testCase.blockedBy + "\n---\n# Title\n"

			err = os.WriteFile(filepath.Join(ticketDir, "test-001.md"), []byte(content), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer

			args := []string{"tk", "-C", tmpDir, "ls"}
			exitCode := Run(nil, &stdout, &stderr, args, nil)

			if testCase.wantErr {
				if exitCode != 1 {
					t.Errorf("exit code = %d, want 1", exitCode)
				}

				if !strings.Contains(stderr.String(), "blocked-by") {
					t.Errorf("stderr should mention blocked-by error\nstderr: %s", stderr.String())
				}
			} else if exitCode != 0 {
				t.Errorf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
			}
		})
	}
}

// createTestTicket creates a test ticket with proper format.
func createTestTicket(t *testing.T, ticketDir, ticketID, status, title string, blockedBy []string) {
	t.Helper()

	blockedByStr := "[]"
	if len(blockedBy) > 0 {
		blockedByStr = "[" + strings.Join(blockedBy, ", ") + "]"
	}

	closedLine := ""
	if status == "closed" {
		closedLine = "closed: " + time.Now().UTC().Format(time.RFC3339) + "\n"
	}

	content := "---\n" +
		"id: " + ticketID + "\n" +
		"status: " + status + "\n" +
		"blocked-by: " + blockedByStr + "\n" +
		"created: 2026-01-04T00:00:00Z\n" +
		"type: task\n" +
		"priority: 2\n" +
		closedLine +
		"---\n" +
		"# " + title + "\n"

	path := filepath.Join(ticketDir, ticketID+".md")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("failed to create test ticket: %v", err)
	}
}

//nolint:funlen,gocognit,cyclop // table-driven test with many cases
func TestLsLimitOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		ticketIDs  []string
		args       []string
		wantExit   int
		wantStdout []string
		notStdout  []string
		wantStderr []string
	}{
		{
			name:       "default limit 100",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"tk", "ls"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002", "c-003"},
			notStdout:  []string{"... and"},
		},
		{
			name:       "limit 2 shows first 2",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"tk", "ls", "--limit=2"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002"},
			notStdout:  []string{"c-003"},
		},
		{
			name:       "offset 1 skips first",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"tk", "ls", "--offset=1"},
			wantExit:   0,
			wantStdout: []string{"b-002", "c-003"},
			notStdout:  []string{"a-001"},
		},
		{
			name:       "limit 1 offset 1",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"tk", "ls", "--limit=1", "--offset=1"},
			wantExit:   0,
			wantStdout: []string{"b-002"},
			notStdout:  []string{"a-001", "c-003"},
		},
		{
			name:       "limit 0 shows all",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"tk", "ls", "--limit=0"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002"},
		},
		{
			name:      "limit 0 no tickets",
			ticketIDs: nil,
			args:      []string{"tk", "ls", "--limit=0"},
			wantExit:  0,
		},
		{
			name:       "offset beyond total errors",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"tk", "ls", "--offset=10"},
			wantExit:   1,
			wantStderr: []string{"offset out of bounds"},
		},
		{
			name:       "offset equals total errors",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"tk", "ls", "--offset=2"},
			wantExit:   1,
			wantStderr: []string{"offset out of bounds"},
		},
		{
			name:       "offset way beyond total errors",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"tk", "ls", "--offset=200000"},
			wantExit:   1,
			wantStderr: []string{"offset out of bounds"},
		},
		{
			name:       "negative limit error",
			ticketIDs:  nil,
			args:       []string{"tk", "ls", "--limit=-1"},
			wantExit:   1,
			wantStderr: []string{"--limit must be non-negative"},
		},
		{
			name:       "negative offset error",
			ticketIDs:  nil,
			args:       []string{"tk", "ls", "--offset=-1"},
			wantExit:   1,
			wantStderr: []string{"--offset must be non-negative"},
		},
		{
			name:       "offset + limit > total shows remaining",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"tk", "ls", "--offset=1", "--limit=10"},
			wantExit:   0,
			wantStdout: []string{"b-002", "c-003"},
			notStdout:  []string{"a-001", "... and"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			ticketDir := filepath.Join(tmpDir, ".tickets")

			if len(testCase.ticketIDs) > 0 {
				err := os.MkdirAll(ticketDir, 0o750)
				if err != nil {
					t.Fatal(err)
				}

				for _, id := range testCase.ticketIDs {
					createTestTicket(t, ticketDir, id, "open", "Ticket "+id, nil)
				}
			}

			args := append([]string{testCase.args[0], "-C", tmpDir}, testCase.args[1:]...)

			var stdout, stderr bytes.Buffer

			exitCode := Run(nil, &stdout, &stderr, args, nil)

			if exitCode != testCase.wantExit {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s",
					exitCode, testCase.wantExit, stdout.String(), stderr.String())
			}

			for _, want := range testCase.wantStdout {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout should contain %q\nstdout: %s", want, stdout.String())
				}
			}

			for _, notWant := range testCase.notStdout {
				if strings.Contains(stdout.String(), notWant) {
					t.Errorf("stdout should NOT contain %q\nstdout: %s", notWant, stdout.String())
				}
			}

			for _, want := range testCase.wantStderr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr should contain %q\nstderr: %s", want, stderr.String())
				}
			}
		})
	}
}

func TestLsLimitWithStatusFilter(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create mixed status tickets
	createTestTicket(t, ticketDir, "a-001", "open", "Open 1", nil)
	createTestTicket(t, ticketDir, "b-002", "closed", "Closed 1", nil)
	createTestTicket(t, ticketDir, "c-003", "open", "Open 2", nil)
	createTestTicket(t, ticketDir, "d-004", "open", "Open 3", nil)

	var stdout, stderr bytes.Buffer

	// Filter by open, then limit to 2
	args := []string{"tk", "-C", tmpDir, "ls", "--status=open", "--limit=2"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
	}

	// Should show first 2 open tickets
	out := stdout.String()
	assertContains(t, out, "a-001")
	assertContains(t, out, "c-003")

	// Should NOT show closed or third open
	if strings.Contains(out, "b-002") {
		t.Error("stdout should NOT contain b-002 (closed)")
	}

	if strings.Contains(out, "d-004") {
		t.Error("stdout should NOT contain d-004 (beyond limit)")
	}
}

func TestLsStatusFilterOffsetOutOfBounds(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create 3 open tickets
	createTestTicket(t, ticketDir, "a-001", "open", "Open 1", nil)
	createTestTicket(t, ticketDir, "b-002", "open", "Open 2", nil)
	createTestTicket(t, ticketDir, "c-003", "open", "Open 3", nil)

	var stdout, stderr bytes.Buffer

	// Filter by open (3 tickets), but offset=10 (out of bounds)
	args := []string{"tk", "-C", tmpDir, "ls", "--status=open", "--offset=10"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 1 {
		t.Errorf("exit code = %d, want 1\nstdout: %s", exitCode, stdout.String())
	}

	if !strings.Contains(stderr.String(), "offset out of bounds") {
		t.Errorf("stderr should contain 'offset out of bounds'\nstderr: %s", stderr.String())
	}
}

func TestLsHelpShowsLimitOffset(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	exitCode := Run(nil, &stdout, &stderr, []string{"tk", "ls", "--help"}, nil)

	if exitCode != 0 {
		t.Errorf("exit code = %d, want 0", exitCode)
	}

	out := stdout.String()
	assertContains(t, out, "--limit")
	assertContains(t, out, "--offset")
	assertContains(t, out, "100")
}

// TestLsColdCacheBuildsFullCache verifies that on cold cache with --limit,
// the full cache is still built (all files processed), then limit applied in memory.
func TestLsColdCacheBuildsFullCache(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create 5 tickets
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		createTestTicket(t, ticketDir, ticketID, "open", "Ticket "+ticketID, nil)
	}

	// Ensure no cache exists
	cachePath := filepath.Join(ticketDir, ".cache")
	_ = os.Remove(cachePath)

	// Run with limit=2 (cold cache)
	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls", "--limit=2"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", exitCode, stderr.String())
	}

	// Should only show 2 tickets
	out := stdout.String()
	assertContains(t, out, "a-001")
	assertContains(t, out, "b-002")

	if strings.Contains(out, "c-003") {
		t.Error("stdout should NOT contain c-003 (beyond limit)")
	}

	// Cache should now exist and contain ALL 5 tickets
	cache, loadErr := LoadCache(ticketDir)
	if loadErr != nil {
		t.Fatalf("cache should exist after first run: %v", loadErr)
	}

	if len(cache.Entries) != 5 {
		t.Errorf("cache should have 5 entries, got %d", len(cache.Entries))
	}

	// Verify all ticket IDs are in cache
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		filename := ticketID + ".md"
		if _, ok := cache.Entries[filename]; !ok {
			t.Errorf("cache should contain %s", filename)
		}
	}
}

// TestLsWarmCacheWithLimit verifies that on warm cache with --limit,
// subsequent runs still return correct results.
func TestLsWarmCacheWithLimit(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create 5 tickets
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		createTestTicket(t, ticketDir, ticketID, "open", "Ticket "+ticketID, nil)
	}

	// First run - builds cache
	var stdout1, stderr1 bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls", "--limit=2"}
	exitCode := Run(nil, &stdout1, &stderr1, args, nil)

	if exitCode != 0 {
		t.Fatalf("first run: exit code = %d, want 0\nstderr: %s", exitCode, stderr1.String())
	}

	// Second run - uses warm cache
	var stdout2, stderr2 bytes.Buffer

	exitCode = Run(nil, &stdout2, &stderr2, args, nil)

	if exitCode != 0 {
		t.Fatalf("second run: exit code = %d, want 0\nstderr: %s", exitCode, stderr2.String())
	}

	// Both runs should produce same output
	if stdout1.String() != stdout2.String() {
		t.Errorf("warm cache should produce same output\nfirst:  %s\nsecond: %s",
			stdout1.String(), stdout2.String())
	}

	// Output should have first 2 tickets
	out := stdout2.String()
	assertContains(t, out, "a-001")
	assertContains(t, out, "b-002")

	if strings.Contains(out, "c-003") {
		t.Error("stdout should NOT contain c-003")
	}
}

// TestLsWarmCacheWithOffset verifies offset works correctly with warm cache.
func TestLsWarmCacheWithOffset(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create 5 tickets
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		createTestTicket(t, ticketDir, ticketID, "open", "Ticket "+ticketID, nil)
	}

	// First run - builds cache (no limit to ensure all cached)
	var stdout1, stderr1 bytes.Buffer

	args1 := []string{"tk", "-C", tmpDir, "ls", "--limit=0"}
	exitCode := Run(nil, &stdout1, &stderr1, args1, nil)

	if exitCode != 0 {
		t.Fatalf("first run: exit code = %d\nstderr: %s", exitCode, stderr1.String())
	}

	// Second run - with offset, uses warm cache
	var stdout2, stderr2 bytes.Buffer

	args2 := []string{"tk", "-C", tmpDir, "ls", "--offset=2", "--limit=2"}
	exitCode = Run(nil, &stdout2, &stderr2, args2, nil)

	if exitCode != 0 {
		t.Fatalf("second run: exit code = %d\nstderr: %s", exitCode, stderr2.String())
	}

	out := stdout2.String()

	// Should skip a-001, b-002 and show c-003, d-004
	if strings.Contains(out, "a-001") {
		t.Error("stdout should NOT contain a-001 (before offset)")
	}

	if strings.Contains(out, "b-002") {
		t.Error("stdout should NOT contain b-002 (before offset)")
	}

	assertContains(t, out, "c-003")
	assertContains(t, out, "d-004")

	if strings.Contains(out, "e-005") {
		t.Error("stdout should NOT contain e-005 (beyond limit)")
	}
}

// TestLsCacheInvalidatedOnFileChange verifies cache is invalidated when file changes.
func TestLsCacheInvalidatedOnFileChange(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create ticket
	createTestTicket(t, ticketDir, "test-001", "open", "Original Title", nil)

	// First run - builds cache
	var stdout1, stderr1 bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls"}
	exitCode := Run(nil, &stdout1, &stderr1, args, nil)

	if exitCode != 0 {
		t.Fatalf("first run failed: %s", stderr1.String())
	}

	assertContains(t, stdout1.String(), "Original Title")

	// Modify the ticket file directly
	time.Sleep(10 * time.Millisecond) // ensure mtime changes

	ticketPath := filepath.Join(ticketDir, "test-001.md")
	content := `---
id: test-001
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Modified Title
`

	writeErr := os.WriteFile(ticketPath, []byte(content), 0o600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	// Second run - should detect mtime change and re-parse
	var stdout2, stderr2 bytes.Buffer

	exitCode = Run(nil, &stdout2, &stderr2, args, nil)

	if exitCode != 0 {
		t.Fatalf("second run failed: %s", stderr2.String())
	}

	assertContains(t, stdout2.String(), "Modified Title")

	if strings.Contains(stdout2.String(), "Original Title") {
		t.Error("should show modified title, not original")
	}
}

// TestLsCacheWithStatusFilter verifies that --status filter processes all files.
func TestLsCacheWithStatusFilter(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create mixed status tickets
	createTestTicket(t, ticketDir, "a-001", "open", "Open 1", nil)
	createTestTicket(t, ticketDir, "b-002", "closed", "Closed 1", nil)
	createTestTicket(t, ticketDir, "c-003", "open", "Open 2", nil)

	// Run with status filter
	var stdout, stderr bytes.Buffer

	args := []string{"tk", "-C", tmpDir, "ls", "--status=open"}
	exitCode := Run(nil, &stdout, &stderr, args, nil)

	if exitCode != 0 {
		t.Fatalf("exit code = %d\nstderr: %s", exitCode, stderr.String())
	}

	out := stdout.String()
	assertContains(t, out, "a-001")
	assertContains(t, out, "c-003")

	if strings.Contains(out, "b-002") {
		t.Error("stdout should NOT contain b-002 (closed)")
	}

	// Cache should contain ALL tickets (not just filtered ones)
	cache, loadErr := LoadCache(ticketDir)
	if loadErr != nil {
		t.Fatalf("cache should exist: %v", loadErr)
	}

	if len(cache.Entries) != 3 {
		t.Errorf("cache should have 3 entries, got %d", len(cache.Entries))
	}
}
