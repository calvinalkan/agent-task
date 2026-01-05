package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tk/internal/cli"
)

func TestShowCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"show"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"show", "nonexistent"},
			wantExit:   1,
			wantStderr: "ticket not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			_, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, tt.wantExit; got != want {
				t.Errorf("exitCode=%d, want=%d", got, want)
			}

			if tt.wantStderr != "" {
				if got, want := stderr, tt.wantStderr; !strings.Contains(got, want) {
					t.Errorf("stderr=%q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestShowCreatedTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test Ticket Title",
		"-d", "This is the description",
		"--design", "Design notes here",
		"--acceptance", "AC here",
	)

	stdout, stderr, exitCode := c.Run("show", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Fatalf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	if got, want := stderr, ""; got != want {
		t.Errorf("stderr=%q, want=%q", got, want)
	}

	cli.AssertContains(t, stdout, "id: "+ticketID)
	cli.AssertContains(t, stdout, "status: open")
	cli.AssertContains(t, stdout, "# Test Ticket Title")
	cli.AssertContains(t, stdout, "This is the description")
	cli.AssertContains(t, stdout, "## Design")
	cli.AssertContains(t, stdout, "Design notes here")
	cli.AssertContains(t, stdout, "## Acceptance Criteria")
	cli.AssertContains(t, stdout, "AC here")
}

func TestShowOnlyReadsFromTicketDir(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	fakeID := "fake-id"

	// Write directly to tmpDir (not .tickets/) to test that show only reads from .tickets/
	err := os.WriteFile(filepath.Join(c.Dir, fakeID+".md"), []byte("fake content"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	stderr := c.MustFail("show", fakeID)
	cli.AssertContains(t, stderr, "ticket not found")
}

func TestShowStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustFail("show", "nonexistent")
}

func TestShowHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"show", "--help"}},
		{name: "short flag", args: []string{"show", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk show")
			cli.AssertContains(t, stdout, "contents")
		})
	}
}
