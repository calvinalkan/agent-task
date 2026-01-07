package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

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

func TestEditorCommand(t *testing.T) {
	t.Parallel()

	t.Run("missing ID returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("editor")
		cli.AssertContains(t, stderr, "ticket ID is required")
	})

	t.Run("ticket not found returns error", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		stderr := c.MustFail("editor", "nonexistent")
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
		stdout, stderr, exitCode := c.Run("editor", ticketID)

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

		stdout, stderr, exitCode := c.Run("editor", ticketID)

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

		_, stderr, exitCode := c.Run("editor", ticketID)

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
}

func TestEditorHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"editor", "--help"}},
		{name: "short flag", args: []string{"editor", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk editor")
			cli.AssertContains(t, stdout, "editor")
		})
	}
}

func TestEditorInMainHelp(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("--help")
	cli.AssertContains(t, stdout, "editor <id>")
}
