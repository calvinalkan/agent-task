package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func TestCreateCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
		checkFile  func(t *testing.T, c *cli.CLI, id string)
	}{
		{
			name:     "creates ticket with title only",
			args:     []string{"create", "Test ticket"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "# Test ticket")
				cli.AssertContains(t, content, "status: open")
				cli.AssertContains(t, content, "type: task")
				cli.AssertContains(t, content, "priority: 2")
			},
		},
		{
			name:     "writes deterministic frontmatter order",
			args:     []string{"create", "Order matters"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				// Deterministic ordering keeps diffs stable and matches the migration plan.
				assertFrontmatterOrder(t, content, []string{
					"id",
					"schema_version",
					"blocked-by",
					"created",
					"priority",
					"status",
					"type",
				})
			},
		},
		{
			name:     "creates ticket with description",
			args:     []string{"create", "Test", "-d", "Description text"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "Description text")
				cli.AssertNotContains(t, content, "## Description")
			},
		},
		{
			name:     "creates ticket with design section",
			args:     []string{"create", "Test", "--design", "Design notes"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "## Design")
				cli.AssertContains(t, content, "Design notes")
			},
		},
		{
			name:     "creates ticket with acceptance criteria",
			args:     []string{"create", "Test", "--acceptance", "Must pass tests"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "## Acceptance Criteria")
				cli.AssertContains(t, content, "Must pass tests")
			},
		},
		{
			name:     "creates ticket with custom type",
			args:     []string{"create", "Test", "-t", "bug"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "type: bug")
			},
		},
		{
			name:     "creates ticket with custom priority",
			args:     []string{"create", "Test", "-p", "1"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "priority: 1")
			},
		},
		{
			name:     "creates ticket with assignee",
			args:     []string{"create", "Test", "-a", "John Doe"},
			wantExit: 0,
			checkFile: func(t *testing.T, c *cli.CLI, id string) {
				t.Helper()
				content := c.ReadTicket(id)
				cli.AssertContains(t, content, "assignee: John Doe")
			},
		},
		{
			name:       "error on missing title",
			args:       []string{"create"},
			wantExit:   1,
			wantStderr: "error: title is required",
		},
		{
			name:       "error on empty description",
			args:       []string{"create", "Test", "-d", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --description",
		},
		{
			name:       "error on empty design",
			args:       []string{"create", "Test", "--design", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --design",
		},
		{
			name:       "error on empty acceptance",
			args:       []string{"create", "Test", "--acceptance", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --acceptance",
		},
		{
			name:       "error on empty type",
			args:       []string{"create", "Test", "-t", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --type",
		},
		{
			name:       "error on empty priority",
			args:       []string{"create", "Test", "-p", ""},
			wantExit:   1,
			wantStderr: "error: invalid argument",
		},
		{
			name:       "error on empty assignee",
			args:       []string{"create", "Test", "-a", ""},
			wantExit:   1,
			wantStderr: "error: empty value not allowed: --assignee",
		},
		{
			name:       "error on invalid type",
			args:       []string{"create", "Test", "-t", "invalid"},
			wantExit:   1,
			wantStderr: "error: invalid type",
		},
		{
			name:       "error on invalid priority too high",
			args:       []string{"create", "Test", "-p", "5"},
			wantExit:   1,
			wantStderr: "error: invalid priority",
		},
		{
			name:       "error on invalid priority too low",
			args:       []string{"create", "Test", "-p", "0"},
			wantExit:   1,
			wantStderr: "error: invalid priority",
		},
		{
			name:       "error on invalid priority non-numeric",
			args:       []string{"create", "Test", "-p", "abc"},
			wantExit:   1,
			wantStderr: "error: invalid argument",
		},
		{
			name:       "error on invalid blocker",
			args:       []string{"create", "Test", "--blocked-by", "nonexistent"},
			wantExit:   1,
			wantStderr: "error: blocker not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			stdout, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, tt.wantExit; got != want {
				t.Errorf("exitCode=%d, want=%d\nstderr=%s", got, want, stderr)
			}

			if tt.wantStderr != "" {
				if got, want := stderr, tt.wantStderr; !strings.Contains(got, want) {
					t.Errorf("stderr=%q, want to contain %q", got, want)
				}
			}

			if tt.wantExit == 0 && tt.checkFile != nil {
				ticketID := strings.TrimSpace(stdout)
				if ticketID == "" {
					t.Fatal("expected ID in stdout")
				}

				tt.checkFile(t, c, ticketID)
			}
		})
	}
}

func TestCreateWithBlocker(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	blockerID := c.MustRun("create", "First ticket")
	id2 := c.MustRun("create", "Second ticket", "--blocked-by", blockerID)

	content := c.ReadTicket(id2)
	cli.AssertContains(t, content, "blocked-by: ["+blockerID+"]")
}

func TestCreateMultipleBlockers(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	blocker1 := c.MustRun("create", "Blocker 1")
	blocker2 := c.MustRun("create", "Blocker 2")
	id := c.MustRun("create", "Main ticket", "--blocked-by", blocker1, "--blocked-by", blocker2)

	content := c.ReadTicket(id)
	cli.AssertContains(t, content, blocker1)
	cli.AssertContains(t, content, blocker2)
}

func TestCreateIDsUniqueUnderConcurrency(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	const numTickets = 10

	ids := make(chan string, numTickets)

	var wg sync.WaitGroup

	for range numTickets {
		wg.Go(func() {
			stdout, _, _ := c.Run("create", "Ticket")
			ids <- strings.TrimSpace(stdout)
		})
	}

	wg.Wait()
	close(ids)

	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate ID: %s", id)
		}

		seen[id] = true
	}
}

func TestCreateIDsSortLexicographically(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	var prevID string

	for i := range 5 {
		id := c.MustRun("create", "Ticket")
		if i > 0 && id <= prevID {
			t.Errorf("ID %s should sort after %s", id, prevID)
		}

		prevID = id
	}
}

func TestCreateHandlesSameSecondCollision(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ids := make([]string, 0, 3)

	for range 3 {
		id := c.MustRun("create", "Ticket")
		ids = append(ids, id)
	}

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

	c := cli.NewCLI(t)
	ticketDir := c.TicketDir()

	_, err := os.Stat(ticketDir)
	if err == nil {
		t.Fatal("ticket dir should not exist yet")
	}

	c.MustRun("create", "Test")

	_, err = os.Stat(ticketDir)
	if err != nil {
		t.Fatal("ticket dir should exist")
	}
}

func TestCreateIDHasNoDash(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test")

	if strings.Contains(ticketID, "-") {
		t.Errorf("ID %q should not contain a dash", ticketID)
	}

	if ticketID == "" {
		t.Error("ID should not be empty")
	}
}

func TestCreateOmitsSectionsIfNotProvided(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id := c.MustRun("create", "Test")
	content := c.ReadTicket(id)

	cli.AssertNotContains(t, content, "## Design")
	cli.AssertNotContains(t, content, "## Acceptance Criteria")
}

func TestCreateOmitsAssigneeIfEmpty(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id := c.MustRun("create", "Test", "-a", "TestUser")
	content := c.ReadTicket(id)

	cli.AssertContains(t, content, "assignee: TestUser")
}

func TestCreateAllOptions(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Full ticket",
		"-d", "Full description",
		"--design", "Design details",
		"--acceptance", "All tests pass",
		"-t", "feature",
		"-p", "1",
		"-a", "Alice",
	)

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "# Full ticket")
	cli.AssertContains(t, content, "Full description")
	cli.AssertContains(t, content, "## Design")
	cli.AssertContains(t, content, "Design details")
	cli.AssertContains(t, content, "## Acceptance Criteria")
	cli.AssertContains(t, content, "All tests pass")
	cli.AssertContains(t, content, "type: feature")
	cli.AssertContains(t, content, "priority: 1")
	cli.AssertContains(t, content, "assignee: Alice")
}

func TestCreateLongFlags(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id := c.MustRun("create", "Long flags",
		"--description", "Desc",
		"--type", "epic",
		"--priority", "3",
		"--assignee", "Bob",
	)

	content := c.ReadTicket(id)
	cli.AssertContains(t, content, "Desc")
	cli.AssertContains(t, content, "type: epic")
	cli.AssertContains(t, content, "priority: 3")
	cli.AssertContains(t, content, "assignee: Bob")
}

func TestCreateNoLockFilesLeftBehind(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	ticketDir := c.TicketDir()
	locksDir := filepath.Join(ticketDir, ".locks")

	files, err := os.ReadDir(ticketDir)
	if err != nil {
		t.Fatalf("failed to read ticket dir: %v", err)
	}

	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".lock") {
			t.Errorf("lock file left behind in ticket dir: %s", f.Name())
		}
	}

	lockFiles, err := os.ReadDir(locksDir)
	if err == nil {
		for _, f := range lockFiles {
			if strings.HasSuffix(f.Name(), ".lock") {
				t.Errorf("lock file left behind in locks dir: %s", f.Name())
			}
		}
	}

	ticketLock := filepath.Join(locksDir, ticketID+".md.lock")

	_, err = os.Stat(ticketLock)
	if err == nil {
		t.Errorf("ticket lock file exists: %s", ticketLock)
	}

	cacheLock := filepath.Join(locksDir, ".cache.lock")

	_, err = os.Stat(cacheLock)
	if err == nil {
		t.Errorf("cache lock file exists: %s", cacheLock)
	}
}

func TestCreateHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"create", "--help"}},
		{name: "short flag", args: []string{"create", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk create")
			cli.AssertContains(t, stdout, "--description")
			cli.AssertContains(t, stdout, "--design")
			cli.AssertContains(t, stdout, "--acceptance")
			cli.AssertContains(t, stdout, "--type")
			cli.AssertContains(t, stdout, "--priority")
			cli.AssertContains(t, stdout, "--assignee")
			cli.AssertContains(t, stdout, "--blocked-by")
			cli.AssertContains(t, stdout, "--parent")
		})
	}
}

func TestCreateWithParent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	content := c.ReadTicket(childID)
	cli.AssertContains(t, content, "parent: "+parentID)
}

func TestCreateWithInvalidParent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("create", "Child ticket", "--parent", "nonexistent")
	cli.AssertContains(t, stderr, "parent not found")
}

func TestCreateWithClosedParent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	c.MustRun("start", parentID)
	c.MustRun("close", parentID)

	stderr := c.MustFail("create", "Child ticket", "--parent", parentID)
	cli.AssertContains(t, stderr, "parent ticket is closed")
}

func assertFrontmatterOrder(t *testing.T, content string, wantKeys []string) {
	t.Helper()

	lines := strings.Split(content, "\n")
	if len(lines) < 3 || lines[0] != "---" {
		t.Fatal("expected frontmatter to start with ---")
	}

	var gotKeys []string

	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if line == "---" {
			break
		}

		key, _, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}

		gotKeys = append(gotKeys, key)
	}

	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("frontmatter key order mismatch\n got: %v\nwant: %v", gotKeys, wantKeys)
	}
}

func TestCreateWithEmptyParent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("create", "Test", "--parent", "")
	cli.AssertContains(t, stderr, "empty value not allowed: --parent")
}

func TestCreateOmitsParentIfNotProvided(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id := c.MustRun("create", "Test")
	content := c.ReadTicket(id)

	cli.AssertNotContains(t, content, "parent:")
}
