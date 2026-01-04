package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const editorHelp = `  editor <id>            Open ticket in users editor`

// resolveEditor checks for an available editor using the env slice.
// Priority: config.Editor -> $EDITOR -> vi -> nano -> error.
func resolveEditor(cfg Config, env []string) (string, error) {
	// 1. Check config.Editor
	if cfg.Editor != "" {
		_, lookErr := exec.LookPath(cfg.Editor)
		if lookErr == nil {
			return cfg.Editor, nil
		}
	}

	// 2. Check $EDITOR from env slice
	for _, e := range env {
		if len(e) > 7 && e[:7] == "EDITOR=" {
			editor := e[7:]
			if editor != "" {
				_, lookErr := exec.LookPath(editor)
				if lookErr == nil {
					return editor, nil
				}
			}
		}
	}

	// 3. Try zed
	_, zedErr := exec.LookPath("zed")
	if zedErr == nil {
		return "zed", nil
	}

	// 4. Try vi
	_, viErr := exec.LookPath("vi")
	if viErr == nil {
		return "vi", nil
	}

	// 5. Try nano
	_, nanoErr := exec.LookPath("nano")
	if nanoErr == nil {
		return "nano", nil
	}

	return "", errNoEditorFound
}

func runEditor(editor, path string, errOut io.Writer) int {
	ctx := context.Background()

	// Build command args - zed needs -w flag to wait
	var cmd *exec.Cmd

	if filepath.Base(editor) == "zed" {
		cmd = exec.CommandContext(ctx, editor, "-n", path)
	} else {
		cmd = exec.CommandContext(ctx, editor, path)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return exitErr.ExitCode()
		}

		fprintln(errOut, "error: failed to run editor:", runErr)

		return 1
	}

	return 0
}

func cmdEditor(
	out io.Writer,
	errOut io.Writer,
	cfg Config,
	workDir string,
	args []string,
	env []string,
) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk editor <id>")
		fprintln(out, "")
		fprintln(out, "Open a ticket in your preferred editor.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", errIDRequired)

		return 1
	}

	ticketID := args[0]

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// Check if ticket exists
	if !TicketExists(ticketDir, ticketID) {
		fprintln(errOut, "error:", errTicketNotFound, ticketID)

		return 1
	}

	path := TicketPath(ticketDir, ticketID)

	// Resolve editor
	editor, resolveErr := resolveEditor(cfg, env)
	if resolveErr != nil {
		fprintln(errOut, "error:", resolveErr)

		return 1
	}

	return runEditor(editor, path, errOut)
}
