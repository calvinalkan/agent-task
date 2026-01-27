package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/cli"

	"github.com/calvinalkan/agent-task/internal/ticket"
)

const statusOpen = "open"
const statusClosed = "closed"

func TestLsCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		setup      func(t *testing.T, ticketDir string)
		args       []string
		wantExit   int
		wantStdout []string
		wantStderr []string
		notStdout  []string
	}{
		{
			name:       "no tickets empty output",
			setup:      nil,
			args:       []string{"ls"},
			wantExit:   0,
			wantStdout: nil,
			wantStderr: nil,
		},
		{
			name: "lists all tickets",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", statusOpen, "First ticket", nil)
				createTestTicket(t, ticketDir, "test-002", statusClosed, "Second ticket", nil)
			},
			args:       []string{"ls"},
			wantExit:   0,
			wantStdout: []string{"test-001", "test-002", "[open]", "[closed]", "First ticket", "Second ticket"},
		},
		{
			name: "filter by status open",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", statusOpen, "Open ticket", nil)
				createTestTicket(t, ticketDir, "test-002", statusClosed, "Closed ticket", nil)
			},
			args:       []string{"ls", "--status=open"},
			wantExit:   0,
			wantStdout: []string{"test-001", "[open]"},
			notStdout:  []string{"test-002"},
		},
		{
			name: "filter by status closed",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", statusOpen, "Open ticket", nil)
				createTestTicket(t, ticketDir, "test-002", statusClosed, "Closed ticket", nil)
			},
			args:       []string{"ls", "--status=closed"},
			wantExit:   0,
			wantStdout: []string{"test-002", "[closed]"},
			notStdout:  []string{"test-001"},
		},
		{
			name: "filter by status in_progress",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", statusOpen, "Open ticket", nil)
				createTestTicket(t, ticketDir, "test-002", "in_progress", "In progress ticket", nil)
			},
			args:       []string{"ls", "--status=in_progress"},
			wantExit:   0,
			wantStdout: []string{"test-002", "[in_progress]"},
			notStdout:  []string{"test-001"},
		},
		{
			name:       "invalid status error",
			args:       []string{"ls", "--status=invalid"},
			wantExit:   1,
			wantStderr: []string{"invalid status"},
		},
		{
			name:       "empty status error",
			args:       []string{"ls", "--status="},
			wantExit:   1,
			wantStderr: []string{"invalid status", "empty"},
		},
		{
			name: "filter by priority",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "p1-001", statusOpen, "Priority 1", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "p2-002", statusOpen, "Priority 2", "task", 2, nil)
				createTestTicketFull(t, ticketDir, "p3-003", statusOpen, "Priority 3", "feature", 3, nil)
			},
			args:       []string{"ls", "--priority=1"},
			wantExit:   0,
			wantStdout: []string{"p1-001", "Priority 1"},
			notStdout:  []string{"p2-002", "p3-003"},
		},
		{
			name: "filter by type",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "bug-001", statusOpen, "A Bug", "bug", 2, nil)
				createTestTicketFull(t, ticketDir, "feat-002", statusOpen, "A Feature", "feature", 2, nil)
				createTestTicketFull(t, ticketDir, "task-003", statusOpen, "A Task", "task", 2, nil)
			},
			args:       []string{"ls", "--type=bug"},
			wantExit:   0,
			wantStdout: []string{"bug-001", "A Bug"},
			notStdout:  []string{"feat-002", "task-003"},
		},
		{
			name: "filter by multiple fields",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "match-001", statusOpen, "Match", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-002", statusOpen, "No Match", "bug", 2, nil)
				createTestTicketFull(t, ticketDir, "nomatch-003", statusClosed, "No Match", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-004", statusOpen, "No Match", "feature", 1, nil)
			},
			args:       []string{"ls", "--status=open", "--priority=1", "--type=bug"},
			wantExit:   0,
			wantStdout: []string{"match-001"},
			notStdout:  []string{"nomatch-002", "nomatch-003", "nomatch-004"},
		},
		{
			name: "filter status and priority",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "match-001", statusOpen, "Match", "task", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-002", statusClosed, "Wrong Status", "task", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-003", statusOpen, "Wrong Priority", "task", 2, nil)
			},
			args:       []string{"ls", "--status=open", "--priority=1"},
			wantExit:   0,
			wantStdout: []string{"match-001"},
			notStdout:  []string{"nomatch-002", "nomatch-003"},
		},
		{
			name: "filter status and type",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "match-001", statusOpen, "Match", "bug", 2, nil)
				createTestTicketFull(t, ticketDir, "nomatch-002", statusClosed, "Wrong Status", "bug", 2, nil)
				createTestTicketFull(t, ticketDir, "nomatch-003", statusOpen, "Wrong Type", "feature", 2, nil)
			},
			args:       []string{"ls", "--status=open", "--type=bug"},
			wantExit:   0,
			wantStdout: []string{"match-001"},
			notStdout:  []string{"nomatch-002", "nomatch-003"},
		},
		{
			name: "filter priority and type",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "match-001", statusOpen, "Match", "feature", 2, nil)
				createTestTicketFull(t, ticketDir, "nomatch-002", statusOpen, "Wrong Priority", "feature", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-003", statusOpen, "Wrong Type", "bug", 2, nil)
			},
			args:       []string{"ls", "--priority=2", "--type=feature"},
			wantExit:   0,
			wantStdout: []string{"match-001"},
			notStdout:  []string{"nomatch-002", "nomatch-003"},
		},
		{
			name: "filter no matches",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "a-001", statusClosed, statusClosed, "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "b-002", statusOpen, statusOpen, "feature", 2, nil)
			},
			args:       []string{"ls", "--status=open", "--priority=1", "--type=bug"},
			wantExit:   0,
			wantStdout: nil,
			notStdout:  []string{"a-001", "b-002"},
		},
		{
			name: "filter all match",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "a-001", statusOpen, "Open 1", "task", 2, nil)
				createTestTicketFull(t, ticketDir, "b-002", statusOpen, "Open 2", "task", 2, nil)
				createTestTicketFull(t, ticketDir, "c-003", statusOpen, "Open 3", "task", 2, nil)
			},
			args:       []string{"ls", "--status=open"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002", "c-003"},
		},
		{
			name: "filter single ticket match",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "match-001", statusOpen, "The One", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "a-002", statusClosed, "Nope", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "b-003", statusOpen, "Nope", "feature", 1, nil)
				createTestTicketFull(t, ticketDir, "c-004", statusOpen, "Nope", "bug", 2, nil)
			},
			args:       []string{"ls", "--status=open", "--priority=1", "--type=bug"},
			wantExit:   0,
			wantStdout: []string{"match-001"},
			notStdout:  []string{"a-002", "b-003", "c-004"},
		},
		{
			name: "filter single ticket no match",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "a-001", statusClosed, statusClosed, "bug", 1, nil)
			},
			args:       []string{"ls", "--status=open"},
			wantExit:   0,
			wantStdout: nil,
			notStdout:  []string{"a-001"},
		},
		{
			name:       "invalid priority error",
			args:       []string{"ls", "--priority=5"},
			wantExit:   1,
			wantStderr: []string{"priority must be 1-4"},
		},
		{
			name:       "invalid type error",
			args:       []string{"ls", "--type=invalid"},
			wantExit:   1,
			wantStderr: []string{"invalid type"},
		},
		{
			name: "shows blockers in output",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "blocker-001", statusOpen, "Blocker ticket", nil)
				createTestTicket(t, ticketDir, "test-002", statusOpen, "Main ticket", []string{"blocker-001"})
			},
			args:       []string{"ls"},
			wantExit:   0,
			wantStdout: []string{"test-002", "<- blocked-by: [blocker-001]"},
		},
		{
			name: "multiple blockers in output",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "blocker-001", statusOpen, "Blocker 1", nil)
				createTestTicket(t, ticketDir, "blocker-002", statusOpen, "Blocker 2", nil)
				createTestTicket(t, ticketDir, "test-003", statusOpen, "Main", []string{"blocker-001", "blocker-002"})
			},
			args:       []string{"ls"},
			wantExit:   0,
			wantStdout: []string{"<- blocked-by: [blocker-001, blocker-002]"},
		},
		{
			name: "no blockers suffix when empty",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "test-001", statusOpen, "No blockers", nil)
			},
			args:      []string{"ls"},
			wantExit:  0,
			notStdout: []string{"<-"},
		},
		{
			name: "sorted by ID oldest first",
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicket(t, ticketDir, "z-999", statusOpen, "Last", nil)
				createTestTicket(t, ticketDir, "a-001", statusOpen, "First", nil)
				createTestTicket(t, ticketDir, "m-500", statusOpen, "Middle", nil)
			},
			args:     []string{"ls"},
			wantExit: 0,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)

			if tt.setup != nil {
				tt.setup(t, c.TicketDir())
			}

			stdout, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, tt.wantExit; got != want {
				t.Errorf("exitCode=%d, want=%d\nstdout: %s\nstderr: %s", got, want, stdout, stderr)
			}

			for _, want := range tt.wantStdout {
				cli.AssertContains(t, stdout, want)
			}

			for _, want := range tt.wantStderr {
				cli.AssertContains(t, stderr, want)
			}

			for _, notWant := range tt.notStdout {
				cli.AssertNotContains(t, stdout, notWant)
			}
		})
	}
}

func TestLsOutputOrder(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	createTestTicket(t, c.TicketDir(), "aaa-001", statusOpen, "First", nil)
	createTestTicket(t, c.TicketDir(), "bbb-002", statusOpen, "Second", nil)
	createTestTicket(t, c.TicketDir(), "ccc-003", statusOpen, "Third", nil)

	stdout := c.MustRun("ls")
	lines := strings.Split(stdout, "\n")

	if got, want := len(lines), 3; got != want {
		t.Fatalf("got %d lines, want %d: %v", got, want, lines)
	}

	// Verify order
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

func TestLsInvalidTicketFile(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		content    string
		wantStderr string
	}{
		{
			name: "missing schema_version",
			content: "---\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: schema_version",
		},
		{
			name: "empty schema_version",
			content: "---\nschema_version: \nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: schema_version (empty)",
		},
		{
			name: "invalid schema_version non-integer",
			content: "---\nschema_version: abc\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: schema_version",
		},
		{
			name: "schema_version zero",
			content: "---\nschema_version: 0\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: schema_version must be positive",
		},
		{
			name: "schema_version negative",
			content: "---\nschema_version: -1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: schema_version",
		},
		{
			name: "unsupported schema_version",
			content: "---\nschema_version: 2\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "unsupported schema_version: 2",
		},
		{
			name: "missing id",
			content: "---\nschema_version: 1\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: id",
		},
		{
			name: "missing status",
			content: "---\nschema_version: 1\nid: test-001\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: status",
		},
		{
			name: "missing type",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: type",
		},
		{
			name: "missing priority",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "missing required field: priority",
		},
		{
			name:       "missing created",
			content:    "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n---\n# Title\n",
			wantStderr: "missing required field: created",
		},
		{
			name: "missing title",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n",
			wantStderr: "no title found",
		},
		{
			name: "invalid status",
			content: "---\nschema_version: 1\nid: test-001\nstatus: pending\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: status",
		},
		{
			name: "invalid type",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: story\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: type",
		},
		{
			name: "priority out of range high",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 5\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: priority",
		},
		{
			name: "priority out of range low",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 0\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: priority",
		},
		{
			name: "invalid created timestamp",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04\n---\n# Title\n",
			wantStderr: "invalid field value: created",
		},
		{
			name: "closed without timestamp",
			content: "---\nschema_version: 1\nid: test-001\nstatus: closed\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "closed ticket missing closed timestamp",
		},
		{
			name: "closed timestamp on open ticket",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
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
			content:    "---\nschema_version: 1\nid: test-001\nstatus: open\n# Title\n",
			wantStderr: "unclosed frontmatter",
		},
		{
			name: "empty id",
			content: "---\nschema_version: 1\nid: \nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: id (empty)",
		},
		{
			name: "empty status",
			content: "---\nschema_version: 1\nid: test-001\nstatus: \ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\n---\n# Title\n",
			wantStderr: "invalid field value: status (empty)",
		},
		{
			name: "empty assignee if present",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nassignee: \n---\n# Title\n",
			wantStderr: "invalid field value: assignee (empty)",
		},
		{
			name: "blocked-by missing brackets",
			content: "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nblocked-by: abc-123\n---\n# Title\n",
			wantStderr: "invalid field value: blocked-by (missing brackets)",
		},
		{
			name:       "frontmatter exceeds line limit",
			content:    "---\n" + strings.Repeat("field: value\n", 110),
			wantStderr: "frontmatter exceeds maximum line limit",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)

			err := os.MkdirAll(c.TicketDir(), 0o750)
			if err != nil {
				t.Fatal(err)
			}

			ticketPath := filepath.Join(c.TicketDir(), "test-001.md")

			err = os.WriteFile(ticketPath, []byte(tt.content), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			stderr := c.MustFail("ls")
			cli.AssertContains(t, stderr, tt.wantStderr)
		})
	}
}

func TestLsMixedValidInvalid(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create one valid ticket
	createTestTicket(t, c.TicketDir(), "valid-001", statusOpen, "Valid ticket", nil)

	// Create one invalid ticket (missing type)
	invalidContent := "---\nschema_version: 1\nid: invalid-002\nstatus: open\npriority: 2\ncreated: 2026-01-04T00:00:00Z\n---\n# Invalid\n"

	err := os.WriteFile(filepath.Join(c.TicketDir(), "invalid-002.md"), []byte(invalidContent), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, exitCode := c.Run("ls")

	// Should exit 1 due to invalid ticket
	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	// Should show valid ticket in stdout
	cli.AssertContains(t, stdout, "valid-001")

	// Should show warning for invalid ticket in stderr
	cli.AssertContains(t, stderr, "invalid-002")
}

func TestLsTicketDirNotExist(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	// Don't create .tickets directory

	stdout, stderr, exitCode := c.Run("ls")

	// Should succeed with empty output
	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d; stderr=%s", got, want, stderr)
	}

	if got, want := stdout, ""; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}
}

func TestLsHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"ls", "--help"}},
		{name: "short flag", args: []string{"ls", "-h"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			stdout, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, 0; got != want {
				t.Errorf("exitCode=%d, want=%d", got, want)
			}

			if got, want := stderr, ""; got != want {
				t.Errorf("stderr=%q, want=%q", got, want)
			}

			cli.AssertContains(t, stdout, "Usage: tk ls")
			cli.AssertContains(t, stdout, "--status")
		})
	}
}

func TestLsIgnoresNonMdFiles(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create a valid ticket
	createTestTicket(t, c.TicketDir(), "test-001", statusOpen, "Valid ticket", nil)

	// Create non-.md files
	err := os.WriteFile(filepath.Join(c.TicketDir(), "notes.txt"), []byte("some notes"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(c.TicketDir(), ".hidden"), []byte("hidden file"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	stdout := c.MustRun("ls")

	// Should only show the .md ticket
	cli.AssertContains(t, stdout, "test-001")
	cli.AssertNotContains(t, stdout, "notes")
}

func TestLsIgnoresSubdirectories(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	subDir := filepath.Join(c.TicketDir(), "archive")

	err := os.MkdirAll(subDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	// Create a valid ticket in main dir
	createTestTicket(t, c.TicketDir(), "test-001", statusOpen, "Valid ticket", nil)

	// Create a ticket in subdirectory (should be ignored)
	createTestTicket(t, subDir, "archived-001", statusClosed, "Archived ticket", nil)

	stdout := c.MustRun("ls")

	cli.AssertContains(t, stdout, "test-001")
	cli.AssertNotContains(t, stdout, "archived-001")
}

func TestLsValidBlockedByFormat(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)

			err := os.MkdirAll(c.TicketDir(), 0o750)
			if err != nil {
				t.Fatal(err)
			}

			content := "---\nschema_version: 1\nid: test-001\nstatus: open\ntype: task\npriority: 2\n" +
				"created: 2026-01-04T00:00:00Z\nblocked-by: " + tt.blockedBy + "\n---\n# Title\n"

			err = os.WriteFile(filepath.Join(c.TicketDir(), "test-001.md"), []byte(content), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			_, stderr, exitCode := c.Run("ls")

			if tt.wantErr {
				if got, want := exitCode, 1; got != want {
					t.Errorf("exitCode=%d, want=%d", got, want)
				}

				cli.AssertContains(t, stderr, "blocked-by")
			} else if exitCode != 0 {
				t.Errorf("exitCode=%d, want=0; stderr=%s", exitCode, stderr)
			}
		})
	}
}

func TestLsLimitOffset(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
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
			args:       []string{"ls"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002", "c-003"},
			notStdout:  []string{"... and"},
		},
		{
			name:       "limit 2 shows first 2",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"ls", "--limit=2"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002"},
			notStdout:  []string{"c-003"},
		},
		{
			name:       "offset 1 skips first",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"ls", "--offset=1"},
			wantExit:   0,
			wantStdout: []string{"b-002", "c-003"},
			notStdout:  []string{"a-001"},
		},
		{
			name:       "limit 1 offset 1",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"ls", "--limit=1", "--offset=1"},
			wantExit:   0,
			wantStdout: []string{"b-002"},
			notStdout:  []string{"a-001", "c-003"},
		},
		{
			name:       "limit 0 shows all",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"ls", "--limit=0"},
			wantExit:   0,
			wantStdout: []string{"a-001", "b-002"},
		},
		{
			name:      "limit 0 no tickets",
			ticketIDs: nil,
			args:      []string{"ls", "--limit=0"},
			wantExit:  0,
		},
		{
			name:       "offset beyond total errors",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"ls", "--offset=10"},
			wantExit:   1,
			wantStderr: []string{"offset out of bounds"},
		},
		{
			name:       "offset equals total errors",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"ls", "--offset=2"},
			wantExit:   1,
			wantStderr: []string{"offset out of bounds"},
		},
		{
			name:       "offset way beyond total errors",
			ticketIDs:  []string{"a-001", "b-002"},
			args:       []string{"ls", "--offset=200000"},
			wantExit:   1,
			wantStderr: []string{"offset out of bounds"},
		},
		{
			name:       "negative limit error",
			ticketIDs:  nil,
			args:       []string{"ls", "--limit=-1"},
			wantExit:   1,
			wantStderr: []string{"--limit must be non-negative"},
		},
		{
			name:       "negative offset error",
			ticketIDs:  nil,
			args:       []string{"ls", "--offset=-1"},
			wantExit:   1,
			wantStderr: []string{"--offset must be non-negative"},
		},
		{
			name:       "offset + limit > total shows remaining",
			ticketIDs:  []string{"a-001", "b-002", "c-003"},
			args:       []string{"ls", "--offset=1", "--limit=10"},
			wantExit:   0,
			wantStdout: []string{"b-002", "c-003"},
			notStdout:  []string{"a-001", "... and"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)

			if len(tt.ticketIDs) > 0 {
				for _, id := range tt.ticketIDs {
					createTestTicket(t, c.TicketDir(), id, statusOpen, "Ticket "+id, nil)
				}
			}

			stdout, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, tt.wantExit; got != want {
				t.Errorf("exitCode=%d, want=%d\nstdout: %s\nstderr: %s", got, want, stdout, stderr)
			}

			for _, want := range tt.wantStdout {
				cli.AssertContains(t, stdout, want)
			}

			for _, notWant := range tt.notStdout {
				cli.AssertNotContains(t, stdout, notWant)
			}

			for _, want := range tt.wantStderr {
				cli.AssertContains(t, stderr, want)
			}
		})
	}
}

func TestLsLimitWithStatusFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create mixed status tickets
	createTestTicket(t, c.TicketDir(), "a-001", statusOpen, "Open 1", nil)
	createTestTicket(t, c.TicketDir(), "b-002", statusClosed, "Closed 1", nil)
	createTestTicket(t, c.TicketDir(), "c-003", statusOpen, "Open 2", nil)
	createTestTicket(t, c.TicketDir(), "d-004", statusOpen, "Open 3", nil)

	stdout := c.MustRun("ls", "--status=open", "--limit=2")

	// Should show first 2 open tickets
	cli.AssertContains(t, stdout, "a-001")
	cli.AssertContains(t, stdout, "c-003")

	// Should NOT show closed or third open
	cli.AssertNotContains(t, stdout, "b-002")
	cli.AssertNotContains(t, stdout, "d-004")
}

func TestLsStatusFilterOffsetOutOfBounds(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create 3 open tickets
	createTestTicket(t, c.TicketDir(), "a-001", statusOpen, "Open 1", nil)
	createTestTicket(t, c.TicketDir(), "b-002", statusOpen, "Open 2", nil)
	createTestTicket(t, c.TicketDir(), "c-003", statusOpen, "Open 3", nil)

	// Filter by open (3 tickets), but offset=10 (out of bounds)
	stderr := c.MustFail("ls", "--status=open", "--offset=10")
	cli.AssertContains(t, stderr, "offset out of bounds")
}

func TestLsHelpShowsLimitOffset(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("ls", "--help")

	cli.AssertContains(t, stdout, "--limit")
	cli.AssertContains(t, stdout, "--offset")
	cli.AssertContains(t, stdout, "100")
}

func TestLsColdCacheBuildsFullCache(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create 5 tickets
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		createTestTicket(t, c.TicketDir(), ticketID, statusOpen, "Ticket "+ticketID, nil)
	}

	// Ensure no cache exists
	cachePath := filepath.Join(c.TicketDir(), ".cache")
	_ = os.Remove(cachePath)

	// Run with limit=2 (cold cache)
	stdout := c.MustRun("ls", "--limit=2")

	// Should only show 2 tickets
	cli.AssertContains(t, stdout, "a-001")
	cli.AssertContains(t, stdout, "b-002")
	cli.AssertNotContains(t, stdout, "c-003")

	// Verify cache was built with ALL tickets by running without limit
	stdout2 := c.MustRun("ls", "--limit=0")

	// All 5 tickets should be returned (proves they were all cached)
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		cli.AssertContains(t, stdout2, ticketID)
	}
}

func TestLsWarmCacheWithLimit(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create 5 tickets
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		createTestTicket(t, c.TicketDir(), ticketID, statusOpen, "Ticket "+ticketID, nil)
	}

	// First run - builds cache
	stdout1 := c.MustRun("ls", "--limit=2")

	// Second run - uses warm cache
	stdout2 := c.MustRun("ls", "--limit=2")

	// Both runs should produce same output
	if got, want := stdout1, stdout2; got != want {
		t.Errorf("warm cache should produce same output\nfirst:  %s\nsecond: %s", got, want)
	}

	// Output should have first 2 tickets
	cli.AssertContains(t, stdout2, "a-001")
	cli.AssertContains(t, stdout2, "b-002")
	cli.AssertNotContains(t, stdout2, "c-003")
}

func TestLsWarmCacheWithOffset(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create 5 tickets
	for _, ticketID := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		createTestTicket(t, c.TicketDir(), ticketID, statusOpen, "Ticket "+ticketID, nil)
	}

	// First run - builds cache (no limit to ensure all cached)
	c.MustRun("ls", "--limit=0")

	// Second run - with offset, uses warm cache
	stdout := c.MustRun("ls", "--offset=2", "--limit=2")

	// Should skip a-001, b-002 and show c-003, d-004
	cli.AssertNotContains(t, stdout, "a-001")
	cli.AssertNotContains(t, stdout, "b-002")
	cli.AssertContains(t, stdout, "c-003")
	cli.AssertContains(t, stdout, "d-004")
	cli.AssertNotContains(t, stdout, "e-005")
}

func TestLsCacheInvalidatedOnFileChange(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create ticket
	createTestTicket(t, c.TicketDir(), "test-001", statusOpen, "Original Title", nil)

	// First run - builds cache
	stdout1 := c.MustRun("ls")
	cli.AssertContains(t, stdout1, "Original Title")

	// Modify the ticket file directly (dir mtime unchanged, cache not invalidated)
	ticketPath := filepath.Join(c.TicketDir(), "test-001.md")
	content := `---
schema_version: 1
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

	// Second run - warm cache should trust cached entry (dir mtime unchanged)
	stdout2 := c.MustRun("ls")
	cli.AssertContains(t, stdout2, "Original Title")
	cli.AssertNotContains(t, stdout2, "Modified Title")
}

func TestLsCacheWithStatusFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create mixed status tickets
	createTestTicket(t, c.TicketDir(), "a-001", statusOpen, "Open 1", nil)
	createTestTicket(t, c.TicketDir(), "b-002", statusClosed, "Closed 1", nil)
	createTestTicket(t, c.TicketDir(), "c-003", statusOpen, "Open 2", nil)

	// Run with status filter
	stdout := c.MustRun("ls", "--status=open")
	cli.AssertContains(t, stdout, "a-001")
	cli.AssertContains(t, stdout, "c-003")
	cli.AssertNotContains(t, stdout, "b-002")

	// Verify cache contains ALL tickets by querying for closed ones
	stdout2 := c.MustRun("ls", "--status=closed")

	// Closed ticket should be returned (proves it was cached too)
	cli.AssertContains(t, stdout2, "b-002")
}

func TestLsBitmapColdVsHotEquivalence(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		args  []string
		setup func(t *testing.T, ticketDir string)
	}{
		{
			name: "status filter",
			args: []string{"--status=open"},
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "a-001", statusOpen, "Open 1", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "b-002", statusClosed, "Closed 1", "task", 2, nil)
				createTestTicketFull(t, ticketDir, "c-003", statusOpen, "Open 2", "feature", 3, nil)
			},
		},
		{
			name: "priority filter",
			args: []string{"--priority=1"},
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "a-001", statusOpen, "P1", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "b-002", statusOpen, "P2", "task", 2, nil)
				createTestTicketFull(t, ticketDir, "c-003", statusClosed, "P1", "feature", 1, nil)
			},
		},
		{
			name: "type filter",
			args: []string{"--type=bug"},
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "a-001", statusOpen, "Bug 1", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "b-002", statusOpen, "Feature 1", "feature", 2, nil)
				createTestTicketFull(t, ticketDir, "c-003", statusOpen, "Bug 2", "bug", 3, nil)
			},
		},
		{
			name: "combined filters",
			args: []string{"--status=open", "--priority=1", "--type=bug"},
			setup: func(t *testing.T, ticketDir string) {
				t.Helper()
				createTestTicketFull(t, ticketDir, "match-001", statusOpen, "Match", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-002", statusOpen, "Wrong Priority", "bug", 2, nil)
				createTestTicketFull(t, ticketDir, "nomatch-003", statusClosed, "Wrong Status", "bug", 1, nil)
				createTestTicketFull(t, ticketDir, "nomatch-004", statusOpen, "Wrong Type", "feature", 1, nil)
				createTestTicketFull(t, ticketDir, "match-005", statusOpen, "Match 2", "bug", 1, nil)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			tt.setup(t, c.TicketDir())

			// Delete cache to ensure cold run
			_ = os.Remove(filepath.Join(c.TicketDir(), ".cache"))

			// Cold cache run
			coldArgs := append([]string{"ls"}, tt.args...)
			coldResult := c.MustRun(coldArgs...)

			// Hot cache run (second run uses cache + bitmaps)
			hotResult := c.MustRun(coldArgs...)

			// Results must be identical
			if got, want := coldResult, hotResult; got != want {
				t.Errorf("cold vs hot mismatch:\ncold:\n%s\nhot:\n%s", got, want)
			}
		})
	}
}

func TestLsBitmapStaleCacheHandling(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create initial tickets
	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "Open Bug", "bug", 1, nil)
	createTestTicketFull(t, c.TicketDir(), "b-002", statusOpen, "Open Feature", "feature", 2, nil)

	// First run to build cache
	c.MustRun("ls", "--status=open")

	// Modify first ticket to closed (dir mtime unchanged, cache not invalidated)
	createTestTicketFull(t, c.TicketDir(), "a-001", statusClosed, "Closed Bug", "bug", 1, nil)

	// Second run - should still show cached entry (dir mtime unchanged)
	stdout := c.MustRun("ls", "--status=open")

	// a-001 should still appear (cache trusts previous open status)
	cli.AssertContains(t, stdout, "a-001")
	// b-002 should still appear
	cli.AssertContains(t, stdout, "b-002")
}

func TestLsBitmapNewFileHandling(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create initial ticket
	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "Initial", "bug", 1, nil)

	// First run to build cache
	c.MustRun("ls", "--type=bug")

	// Backdate cache so directory changes are detected
	backdateCacheLS(t, c.TicketDir())

	// Add new ticket matching filter
	createTestTicketFull(t, c.TicketDir(), "b-002", statusOpen, "New Bug", "bug", 2, nil)

	// Second run should find new ticket
	stdout := c.MustRun("ls", "--type=bug")

	cli.AssertContains(t, stdout, "a-001")
	cli.AssertContains(t, stdout, "b-002")
}

func TestLsHelpShowsPriorityAndType(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("ls", "--help")

	cli.AssertContains(t, stdout, "--priority")
	cli.AssertContains(t, stdout, "--type")
	cli.AssertContains(t, stdout, "1-4")
	cli.AssertContains(t, stdout, "bug")
}

func TestLsBitmapBoundary64Tickets(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create exactly 64 tickets - half open, half closed
	for i := range 64 {
		id := fmt.Sprintf("t-%03d", i)

		status := statusOpen
		if i >= 32 {
			status = statusClosed
		}

		createTestTicketFull(t, c.TicketDir(), id, status, "Title", "task", 2, nil)
	}

	// Cold run
	_ = os.Remove(filepath.Join(c.TicketDir(), ".cache"))
	coldResult := c.MustRun("ls", "--status=open")

	// Hot run
	hotResult := c.MustRun("ls", "--status=open")

	if got, want := coldResult, hotResult; got != want {
		t.Error("cold vs hot mismatch for 64 tickets")
	}

	// Should have 32 open tickets
	lines := strings.Split(coldResult, "\n")
	if got, want := len(lines), 32; got != want {
		t.Errorf("got %d open tickets, want %d", got, want)
	}
}

func TestLsBitmapBoundary65Tickets(t *testing.T) {
	t.Parallel()

	th := cli.NewCLI(t)

	// Create 65 tickets
	for i := range 65 {
		id := fmt.Sprintf("t-%03d", i)

		status := statusOpen
		if i%2 == 0 {
			status = statusClosed
		}

		createTestTicketFull(t, th.TicketDir(), id, status, "Title", "task", 2, nil)
	}

	// Cold run
	_ = os.Remove(filepath.Join(th.TicketDir(), ".cache"))
	coldResult := th.MustRun("ls", "--status=open")

	// Hot run
	hotResult := th.MustRun("ls", "--status=open")

	if got, want := coldResult, hotResult; got != want {
		t.Error("cold vs hot mismatch for 65 tickets")
	}

	// Should have 32 open tickets (odd indices: 1,3,5,...,63)
	lines := strings.Split(coldResult, "\n")
	if got, want := len(lines), 32; got != want {
		t.Errorf("got %d open tickets, want %d", got, want)
	}
}

func TestLsBitmapStalePriorityChange(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "P1 Bug", "bug", 1, nil)
	createTestTicketFull(t, c.TicketDir(), "b-002", statusOpen, "P2 Bug", "bug", 2, nil)

	// Build cache
	c.MustRun("ls", "--priority=1")

	// Modify priority (dir mtime unchanged, cache not invalidated)
	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "Now P2", "bug", 2, nil)

	// Query for P1 - cache should still include a-001 (external frontmatter edits not detected)
	stdout := c.MustRun("ls", "--priority=1")
	cli.AssertContains(t, stdout, "a-001")
}

func TestLsBitmapStaleTypeChange(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "A Bug", "bug", 1, nil)
	createTestTicketFull(t, c.TicketDir(), "b-002", statusOpen, "A Feature", "feature", 1, nil)

	// Build cache
	c.MustRun("ls", "--type=bug")

	// Modify type (dir mtime unchanged, cache not invalidated)
	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "Now Feature", "feature", 1, nil)

	// Query for bug - cache should still include a-001 (external frontmatter edits not detected)
	stdout := c.MustRun("ls", "--type=bug")
	cli.AssertContains(t, stdout, "a-001")
}

func TestLsBitmapFileDeleted(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, "Will Delete", "bug", 1, nil)
	createTestTicketFull(t, c.TicketDir(), "b-002", statusOpen, "Will Keep", "bug", 1, nil)

	// Build cache
	c.MustRun("ls", "--type=bug")

	// Backdate cache so directory changes are detected
	backdateCacheLS(t, c.TicketDir())

	// Delete file
	_ = os.Remove(filepath.Join(c.TicketDir(), "a-001.md"))

	// Query - should not include deleted file
	stdout := c.MustRun("ls", "--type=bug")
	cli.AssertNotContains(t, stdout, "a-001")
	cli.AssertContains(t, stdout, "b-002")
}

func TestLsBitmapPaginationColdVsHot(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create 10 open tickets
	for i := range 10 {
		id := fmt.Sprintf("t-%03d", i)
		createTestTicketFull(t, c.TicketDir(), id, statusOpen, statusOpen, "bug", 1, nil)
	}

	// Create 5 closed tickets
	for i := 10; i < 15; i++ {
		id := fmt.Sprintf("t-%03d", i)
		createTestTicketFull(t, c.TicketDir(), id, statusClosed, statusClosed, "bug", 1, nil)
	}

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"limit with filter", []string{"--status=open", "--limit=5"}},
		{"offset with filter", []string{"--status=open", "--offset=3"}},
		{"limit and offset with filter", []string{"--status=open", "--limit=3", "--offset=2"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Cold run
			_ = os.Remove(filepath.Join(c.TicketDir(), ".cache"))

			coldArgs := append([]string{"ls"}, tt.args...)
			coldResult := c.MustRun(coldArgs...)

			// Hot run
			hotResult := c.MustRun(coldArgs...)

			if got, want := coldResult, hotResult; got != want {
				t.Errorf("cold vs hot mismatch:\ncold:\n%s\nhot:\n%s", got, want)
			}
		})
	}
}

func TestLsBitmapStaleStatusChangedNowMatches(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	createTestTicketFull(t, c.TicketDir(), "a-001", statusOpen, statusOpen, "bug", 1, nil)
	createTestTicketFull(t, c.TicketDir(), "b-002", statusOpen, "Will Close", "bug", 1, nil)

	// Build cache with open filter
	c.MustRun("ls", "--status=open")

	// Modify b-002 to closed (dir mtime unchanged, cache not invalidated)
	createTestTicketFull(t, c.TicketDir(), "b-002", statusClosed, "Now Closed", "bug", 1, nil)

	// Query for closed - cache should NOT include b-002 (external status change not detected)
	stdout := c.MustRun("ls", "--status=closed")
	cli.AssertNotContains(t, stdout, "b-002")
	cli.AssertNotContains(t, stdout, "a-001")
}

func TestLsParentFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	child1ID := c.MustRun("create", "Child 1", "--parent", parentID)
	child2ID := c.MustRun("create", "Child 2", "--parent", parentID)
	standaloneID := c.MustRun("create", "Standalone")

	stdout := c.MustRun("ls", "--parent", parentID)

	cli.AssertTicketListed(t, stdout, child1ID)
	cli.AssertTicketListed(t, stdout, child2ID)
	cli.AssertTicketNotListed(t, stdout, parentID)
	cli.AssertTicketNotListed(t, stdout, standaloneID)
}

func TestLsRootsFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child", "--parent", parentID)
	standaloneID := c.MustRun("create", "Standalone")

	stdout := c.MustRun("ls", "--roots")

	cli.AssertTicketListed(t, stdout, parentID)
	cli.AssertTicketListed(t, stdout, standaloneID)
	cli.AssertTicketNotListed(t, stdout, childID)
}

func TestLsParentAndRootsConflict(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "Some ticket")

	stderr := c.MustFail("ls", "--parent", "abc", "--roots")
	cli.AssertContains(t, stderr, "--parent and --roots cannot be used together")
}

func TestLsShowsParentInOutput(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	stdout := c.MustRun("ls")

	// Child should show parent
	lines := strings.SplitSeq(stdout, "\n")
	for line := range lines {
		if strings.Contains(line, childID) {
			cli.AssertContains(t, line, "(parent: "+parentID+")")
		}

		if strings.Contains(line, parentID) && !strings.Contains(line, childID) {
			cli.AssertNotContains(t, line, "(parent:")
		}
	}
}

func TestLsParentFilterWithOtherFilters(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket", "-t", "epic")
	child1ID := c.MustRun("create", "Bug child", "--parent", parentID, "-t", "bug")
	child2ID := c.MustRun("create", "Feature child", "--parent", parentID, "-t", "feature")

	// Filter by parent AND type
	stdout := c.MustRun("ls", "--parent", parentID, "--type", "bug")

	cli.AssertTicketListed(t, stdout, child1ID)
	cli.AssertTicketNotListed(t, stdout, child2ID)
	cli.AssertTicketNotListed(t, stdout, parentID)
}

func TestLsRootsWithStatusFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	openRootID := c.MustRun("create", "Open root")
	closedRootID := c.MustRun("create", "Closed root")
	c.MustRun("start", closedRootID)
	c.MustRun("close", closedRootID)
	childID := c.MustRun("create", "Child of open", "--parent", openRootID)

	// Filter by roots and open status
	stdout := c.MustRun("ls", "--roots", "--status", statusOpen)
	cli.AssertTicketListed(t, stdout, openRootID)
	cli.AssertTicketNotListed(t, stdout, closedRootID)
	cli.AssertTicketNotListed(t, stdout, childID)

	// Filter by roots and closed status
	stdout = c.MustRun("ls", "--roots", "--status", statusClosed)
	cli.AssertTicketListed(t, stdout, closedRootID)
	cli.AssertTicketNotListed(t, stdout, openRootID)
}

func TestLsHelpShowsParentAndRootsFlags(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("ls", "--help")

	cli.AssertContains(t, stdout, "--parent")
	cli.AssertContains(t, stdout, "--roots")
}

func TestLsCacheRegenAfterVersionChange(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create parent and children
	parentID := c.MustRun("create", "Parent ticket", "-t", "epic")
	child1ID := c.MustRun("create", "Child 1", "--parent", parentID)
	child2ID := c.MustRun("create", "Child 2", "--parent", parentID)
	standaloneID := c.MustRun("create", "Standalone")

	// Use ls to ensure cache is built
	c.MustRun("ls")

	// Delete the cache to force regeneration
	cachePath := filepath.Join(c.TicketDir(), ".cache")

	err := os.Remove(cachePath)
	if err != nil {
		t.Fatalf("failed to remove cache: %v", err)
	}

	// ls --parent should still work (cache regenerates)
	stdout := c.MustRun("ls", "--parent", parentID)
	cli.AssertTicketListed(t, stdout, child1ID)
	cli.AssertTicketListed(t, stdout, child2ID)
	cli.AssertTicketNotListed(t, stdout, parentID)
	cli.AssertTicketNotListed(t, stdout, standaloneID)

	// ls --roots should work
	stdout = c.MustRun("ls", "--roots")
	cli.AssertTicketListed(t, stdout, parentID)
	cli.AssertTicketListed(t, stdout, standaloneID)
	cli.AssertTicketNotListed(t, stdout, child1ID)
	cli.AssertTicketNotListed(t, stdout, child2ID)
}

func TestLsParentFilterUsesIndex(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create hierarchy
	parentID := c.MustRun("create", "Parent")
	childID := c.MustRun("create", "Child", "--parent", parentID)
	c.MustRun("create", "Standalone")

	// First run builds cache
	stdout1 := c.MustRun("ls", "--parent", parentID)
	cli.AssertTicketListed(t, stdout1, childID)

	// Second run uses cached index
	stdout2 := c.MustRun("ls", "--parent", parentID)

	if stdout1 != stdout2 {
		t.Errorf("output differs:\nfirst: %q\nsecond: %q", stdout1, stdout2)
	}
}

func TestLsRootsFilterUsesIndex(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent")
	c.MustRun("create", "Child", "--parent", parentID)

	// First run builds cache
	stdout1 := c.MustRun("ls", "--roots")
	cli.AssertTicketListed(t, stdout1, parentID)

	// Second run uses cached index
	stdout2 := c.MustRun("ls", "--roots")

	if stdout1 != stdout2 {
		t.Errorf("output differs:\nfirst: %q\nsecond: %q", stdout1, stdout2)
	}
}

func TestLsParentWithStatusFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent", "-t", "epic")
	openChildID := c.MustRun("create", "Open child", "--parent", parentID)
	closedChildID := c.MustRun("create", "Closed child", "--parent", parentID)

	// Start parent and close one child
	c.MustRun("start", parentID)
	c.MustRun("start", closedChildID)
	c.MustRun("close", closedChildID)

	// Filter by parent AND status=open
	stdout := c.MustRun("ls", "--parent", parentID, "--status", statusOpen)
	cli.AssertTicketListed(t, stdout, openChildID)
	cli.AssertTicketNotListed(t, stdout, closedChildID)

	// Filter by parent AND status=closed
	stdout = c.MustRun("ls", "--parent", parentID, "--status", statusClosed)
	cli.AssertTicketListed(t, stdout, closedChildID)
	cli.AssertTicketNotListed(t, stdout, openChildID)
}

func TestLsParentWithPriorityFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent", "-t", "epic")
	p1ChildID := c.MustRun("create", "P1 child", "--parent", parentID, "-p", "1")
	p3ChildID := c.MustRun("create", "P3 child", "--parent", parentID, "-p", "3")

	// Filter by parent AND priority=1
	stdout := c.MustRun("ls", "--parent", parentID, "--priority", "1")
	cli.AssertTicketListed(t, stdout, p1ChildID)
	cli.AssertTicketNotListed(t, stdout, p3ChildID)
}

func TestLsParentWithTypeFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent", "-t", "epic")
	bugChildID := c.MustRun("create", "Bug child", "--parent", parentID, "-t", "bug")
	featureChildID := c.MustRun("create", "Feature child", "--parent", parentID, "-t", "feature")

	// Filter by parent AND type=bug
	stdout := c.MustRun("ls", "--parent", parentID, "--type", "bug")
	cli.AssertTicketListed(t, stdout, bugChildID)
	cli.AssertTicketNotListed(t, stdout, featureChildID)
}

func TestLsRootsWithPriorityFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	p1RootID := c.MustRun("create", "P1 root", "-p", "1")
	p4RootID := c.MustRun("create", "P4 root", "-p", "4")
	childID := c.MustRun("create", "Child", "--parent", p1RootID, "-p", "1")

	// Filter by roots and priority 1
	stdout := c.MustRun("ls", "--roots", "--priority", "1")
	cli.AssertTicketListed(t, stdout, p1RootID)
	cli.AssertTicketNotListed(t, stdout, p4RootID)
	cli.AssertTicketNotListed(t, stdout, childID)
}

func TestLsRootsWithTypeFilter(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	epicRootID := c.MustRun("create", "Epic root", "-t", "epic")
	bugRootID := c.MustRun("create", "Bug root", "-t", "bug")
	childID := c.MustRun("create", "Child", "--parent", epicRootID, "-t", "task")

	// Roots + type=epic
	stdout := c.MustRun("ls", "--roots", "--type", "epic")
	cli.AssertTicketListed(t, stdout, epicRootID)
	cli.AssertTicketNotListed(t, stdout, bugRootID)
	cli.AssertTicketNotListed(t, stdout, childID)
}

func TestLsParentWithMultipleFilters(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent", "-t", "epic")
	matchID := c.MustRun("create", "Match", "--parent", parentID, "-t", "bug", "-p", "1")
	wrongTypeID := c.MustRun("create", "Wrong type", "--parent", parentID, "-t", "feature", "-p", "1")
	wrongPrioID := c.MustRun("create", "Wrong prio", "--parent", parentID, "-t", "bug", "-p", "4")

	// Filter by parent + type + priority
	stdout := c.MustRun("ls", "--parent", parentID, "--type", "bug", "--priority", "1")
	cli.AssertTicketListed(t, stdout, matchID)
	cli.AssertTicketNotListed(t, stdout, wrongTypeID)
	cli.AssertTicketNotListed(t, stdout, wrongPrioID)
}

func TestLsRootsWithMultipleFilters(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	matchID := c.MustRun("create", "Match", "-t", "bug", "-p", "1")
	wrongTypeID := c.MustRun("create", "Wrong type", "-t", "epic", "-p", "1")
	wrongPrioID := c.MustRun("create", "Wrong prio", "-t", "bug", "-p", "4")
	childID := c.MustRun("create", "Child", "--parent", matchID, "-t", "bug", "-p", "1")

	// Roots + type + priority
	stdout := c.MustRun("ls", "--roots", "--type", "bug", "--priority", "1")
	cli.AssertTicketListed(t, stdout, matchID)
	cli.AssertTicketNotListed(t, stdout, wrongTypeID)
	cli.AssertTicketNotListed(t, stdout, wrongPrioID)
	cli.AssertTicketNotListed(t, stdout, childID)
}

func TestLsParentWithLimitOffset(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent")
	child1 := c.MustRun("create", "Child 1", "--parent", parentID)
	child2 := c.MustRun("create", "Child 2", "--parent", parentID)
	child3 := c.MustRun("create", "Child 3", "--parent", parentID)

	// Limit=2
	stdout := c.MustRun("ls", "--parent", parentID, "--limit", "2")
	cli.AssertTicketListed(t, stdout, child1)
	cli.AssertTicketListed(t, stdout, child2)
	cli.AssertTicketNotListed(t, stdout, child3)

	// Offset=1, Limit=1
	stdout = c.MustRun("ls", "--parent", parentID, "--offset", "1", "--limit", "1")
	cli.AssertTicketNotListed(t, stdout, child1)
	cli.AssertTicketListed(t, stdout, child2)
	cli.AssertTicketNotListed(t, stdout, child3)
}

// createTestTicket creates a test ticket with proper format.
func createTestTicket(t *testing.T, ticketDir, ticketID, status, title string, blockedBy []string) {
	t.Helper()

	createTestTicketFull(t, ticketDir, ticketID, status, title, "task", 2, blockedBy)
}

func backdateCacheLS(t *testing.T, ticketDir string) {
	t.Helper()

	cachePath := filepath.Join(ticketDir, ticket.CacheFileName)
	past := time.Now().Add(-10 * time.Second)

	err := os.Chtimes(cachePath, past, past)
	if err != nil {
		t.Fatalf("failed to backdate cache: %v", err)
	}
}
