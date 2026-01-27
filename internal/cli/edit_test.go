package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func Test_Edit_Launch_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	t.Run("missing ID returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("edit", "--launch")
		cli.AssertContains(t, stderr, "ticket ID is required")
	})

	t.Run("ticket not found returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("edit", "nonexistent", "--launch")
		cli.AssertContains(t, stderr, "ticket not found")
	})

	t.Run("config editor used first over EDITOR env", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		mockEditor, invokedFile := createMockEditor(t)

		ticketID := c.MustRun("create", "Test ticket.Ticket")

		// Write config with editor
		cfgPath := filepath.Join(c.Dir, ".tk.json")
		cfgContent := `{"editor": "` + mockEditor + `"}`

		cfgErr := os.WriteFile(cfgPath, []byte(cfgContent), 0o600)
		if cfgErr != nil {
			t.Fatalf("failed to write config: %v", cfgErr)
		}

		// Provide a different EDITOR that should NOT be used
		c.Env["EDITOR"] = "/should/not/use/this"
		stdout, stderr, exitCode := c.Run("edit", ticketID, "--launch")

		if got, want := exitCode, 0; got != want {
			t.Errorf("exitCode=%d, want=%d; stderr=%s", got, want, stderr)
		}

		_ = stdout // stdout not checked in this test

		// Verify mock editor was called with correct path
		invoked, readErr := os.ReadFile(invokedFile)
		if readErr != nil {
			t.Fatalf("mock editor was not called: %v", readErr)
		}

		expectedPath := filepath.Join(c.Dir, ".tickets", ticketID+".md")
		cli.AssertContains(t, string(invoked), expectedPath)
	})

	t.Run("EDITOR env fallback when no config editor", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		mockEditor, invokedFile := createMockEditor(t)

		ticketID := c.MustRun("create", "Test ticket.Ticket")

		// No config editor set - also disable global config by setting XDG_CONFIG_HOME to temp dir
		c.Env["EDITOR"] = mockEditor
		c.Env["XDG_CONFIG_HOME"] = c.Dir // Prevents loading ~/.config/tk/config.json

		stdout, stderr, exitCode := c.Run("edit", ticketID, "--launch")

		if got, want := exitCode, 0; got != want {
			t.Errorf("exitCode=%d, want=%d; stderr=%s", got, want, stderr)
		}

		_ = stdout // stdout not checked in this test

		// Verify mock editor was called
		invoked, readErr := os.ReadFile(invokedFile)
		if readErr != nil {
			t.Fatalf("mock editor was not called: %v", readErr)
		}

		expectedPath := filepath.Join(c.Dir, ".tickets", ticketID+".md")
		cli.AssertContains(t, string(invoked), expectedPath)
	})

	t.Run("correct file path passed to editor", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		mockEditor, invokedFile := createMockEditor(t)

		ticketID := c.MustRun("create", "Test ticket.Ticket")

		c.Env["EDITOR"] = mockEditor
		c.Env["XDG_CONFIG_HOME"] = c.Dir // Prevents loading ~/.config/tk/config.json

		_, stderr, exitCode := c.Run("edit", ticketID, "--launch")

		if got, want := exitCode, 0; got != want {
			t.Errorf("exitCode=%d, want=%d; stderr=%s", got, want, stderr)
		}

		// Verify correct path
		invoked, readErr := os.ReadFile(invokedFile)
		if readErr != nil {
			t.Fatalf("mock editor was not called: %v", readErr)
		}

		expectedPath := filepath.Join(c.Dir, ".tickets", ticketID+".md")

		if got, want := strings.TrimSpace(string(invoked)), expectedPath; got != want {
			t.Errorf("editor path=%q, want=%q", got, want)
		}
	})

	t.Run("short flag -l works", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		mockEditor, invokedFile := createMockEditor(t)

		ticketID := c.MustRun("create", "Test ticket.Ticket")

		c.Env["EDITOR"] = mockEditor
		c.Env["XDG_CONFIG_HOME"] = c.Dir

		_, stderr, exitCode := c.Run("edit", ticketID, "-l")

		if got, want := exitCode, 0; got != want {
			t.Errorf("exitCode=%d, want=%d; stderr=%s", got, want, stderr)
		}

		// Verify mock editor was called
		_, readErr := os.ReadFile(invokedFile)
		if readErr != nil {
			t.Fatalf("mock editor was not called: %v", readErr)
		}
	})
}

func Test_Edit_Start_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	t.Run("missing ID returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("edit", "--start")
		cli.AssertContains(t, stderr, "ticket ID is required")
	})

	t.Run("ticket not found returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("edit", "nonexistent", "--start")
		cli.AssertContains(t, stderr, "ticket not found")
	})

	t.Run("creates temp file with body", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket for editing")

		stdout := c.MustRun("edit", ticketID, "--start")

		// Should show frontmatter
		cli.AssertContains(t, stdout, "---")
		cli.AssertContains(t, stdout, "id: "+ticketID)
		cli.AssertContains(t, stdout, "status: open")

		// Should show instructions
		cli.AssertContains(t, stdout, "Body copied to:")
		cli.AssertContains(t, stdout, "tk-"+ticketID+".edit.md")
		cli.AssertContains(t, stdout, "Edit the file, then run: tk edit "+ticketID+" --apply")
		cli.AssertContains(t, stdout, "Note: Only body content can be edited")

		// Temp file should exist with body content (in test's TMPDIR = c.Dir)
		tempPath := filepath.Join(c.Dir, "tk-"+ticketID+".edit.md")

		content, err := os.ReadFile(tempPath)
		if err != nil {
			t.Fatalf("temp file not created: %v", err)
		}

		cli.AssertContains(t, string(content), "# Test ticket for editing")
	})

	t.Run("fails if edit already in progress", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		// Start first edit
		c.MustRun("edit", ticketID, "--start")

		// Try to start again
		stderr := c.MustFail("edit", ticketID, "--start")
		cli.AssertContains(t, stderr, "edit already in progress")
	})

	t.Run("fails if stale edit exists", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		// Create a stale temp file (old mtime) in test's TMPDIR
		tempPath := filepath.Join(c.Dir, "tk-"+ticketID+".edit.md")

		err := os.WriteFile(tempPath, []byte("# Old content"), 0o600)
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}

		// Set mtime to 2 hours ago
		oldTime := time.Now().Add(-2 * time.Hour)
		_ = os.Chtimes(tempPath, oldTime, oldTime)

		stderr := c.MustFail("edit", ticketID, "--start")
		cli.AssertContains(t, stderr, "stale edit found")
	})
}

func Test_Edit_Apply_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	t.Run("missing ID returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("edit", "--apply")
		cli.AssertContains(t, stderr, "ticket ID is required")
	})

	t.Run("ticket not found returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("edit", "nonexistent", "--apply")
		cli.AssertContains(t, stderr, "ticket not found")
	})

	t.Run("fails if no edit in progress", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		stderr := c.MustFail("edit", ticketID, "--apply")
		cli.AssertContains(t, stderr, "no edit in progress")
	})

	t.Run("fails if edit is stale", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		// Create a stale temp file in test's TMPDIR
		tempPath := filepath.Join(c.Dir, "tk-"+ticketID+".edit.md")

		err := os.WriteFile(tempPath, []byte("# Content"), 0o600)
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}

		oldTime := time.Now().Add(-2 * time.Hour)
		_ = os.Chtimes(tempPath, oldTime, oldTime)

		stderr := c.MustFail("edit", ticketID, "--apply")
		cli.AssertContains(t, stderr, "stale edit found")
	})

	t.Run("fails if body is empty", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		// Create empty temp file in test's TMPDIR
		tempPath := filepath.Join(c.Dir, "tk-"+ticketID+".edit.md")

		err := os.WriteFile(tempPath, []byte(""), 0o600)
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}

		stderr := c.MustFail("edit", ticketID, "--apply")
		cli.AssertContains(t, stderr, "body cannot be empty")
	})

	t.Run("fails if body has no heading", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		// Create temp file without heading in test's TMPDIR
		tempPath := filepath.Join(c.Dir, "tk-"+ticketID+".edit.md")

		err := os.WriteFile(tempPath, []byte("No heading here, just text."), 0o600)
		if err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}

		stderr := c.MustFail("edit", ticketID, "--apply")
		cli.AssertContains(t, stderr, "body must contain a heading")
	})

	t.Run("successfully applies edit", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Original title")

		// Start edit
		c.MustRun("edit", ticketID, "--start")

		// Modify temp file in test's TMPDIR
		tempPath := filepath.Join(c.Dir, "tk-"+ticketID+".edit.md")
		newBody := "# Updated title\n\nNew description here.\n\n## Acceptance Criteria\n\n- Item 1\n- Item 2"

		err := os.WriteFile(tempPath, []byte(newBody), 0o600)
		if err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}

		// Apply edit
		stdout := c.MustRun("edit", ticketID, "--apply")
		cli.AssertContains(t, stdout, "Updated ticket "+ticketID)

		// Verify ticket was updated
		showOutput := c.MustRun("show", ticketID)
		cli.AssertContains(t, showOutput, "# Updated title")
		cli.AssertContains(t, showOutput, "New description here")
		cli.AssertContains(t, showOutput, "## Acceptance Criteria")

		// Verify frontmatter preserved
		cli.AssertContains(t, showOutput, "id: "+ticketID)
		cli.AssertContains(t, showOutput, "status: open")

		// Temp file should be deleted
		_, statErr := os.Stat(tempPath)
		if !os.IsNotExist(statErr) {
			t.Error("temp file should have been deleted after apply")
		}
	})
}

func Test_Edit_Mode_Flags_When_Invoked(t *testing.T) {
	t.Parallel()

	t.Run("no mode flag returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		stderr := c.MustFail("edit", ticketID)
		cli.AssertContains(t, stderr, "must specify --start, --apply, or --launch")
	})

	t.Run("multiple mode flags returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "Test ticket")

		stderr := c.MustFail("edit", ticketID, "--start", "--apply")
		cli.AssertContains(t, stderr, "mutually exclusive")
	})
}

func Test_Edit_Help_When_Invoked(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"edit", "--help"}},
		{name: "short flag", args: []string{"edit", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk edit")
			cli.AssertContains(t, stdout, "--start")
			cli.AssertContains(t, stdout, "--apply")
			cli.AssertContains(t, stdout, "--launch")
		})
	}
}

func Test_Edit_In_Main_Help_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("--help")
	cli.AssertContains(t, stdout, "edit <id>")
}

// createMockEditor creates a mock editor script that writes its args to a file.
// Returns the path to the mock editor and the path to the invoked args file.
func createMockEditor(t *testing.T) (string, string) {
	t.Helper()

	mockDir := t.TempDir()
	mockEditor := filepath.Join(mockDir, "mock-editor")
	invokedFile := filepath.Join(mockDir, "invoked.txt")

	script := `#!/bin/sh
echo "$@" > "` + invokedFile + `"
exit 0
`

	writeErr := os.WriteFile(mockEditor, []byte(script), 0o700)
	if writeErr != nil {
		t.Fatalf("failed to create mock editor: %v", writeErr)
	}

	return mockEditor, invokedFile
}
