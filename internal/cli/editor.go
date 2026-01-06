package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"tk/internal/ticket"
)

const editorHelp = `  editor <id>            Open ticket in users editor`

// resolveEditor checks for an available editor using the env map.
// Priority: config.Editor -> $EDITOR -> zed -> vi -> nano -> error.
func resolveEditor(cfg ticket.Config, env map[string]string) (string, error) {
	// 1. Check config.Editor
	if cfg.Editor != "" {
		_, lookErr := exec.LookPath(cfg.Editor)
		if lookErr == nil {
			return cfg.Editor, nil
		}
	}

	// 2. Check $EDITOR from env map
	if editor := env["EDITOR"]; editor != "" {
		_, lookErr := exec.LookPath(editor)
		if lookErr == nil {
			return editor, nil
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

	return "", ticket.ErrNoEditorFound
}

func runEditor(editor, path string) error {
	ctx := context.Background()

	// Build command args - zed needs -n flag
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
			return fmt.Errorf("editor exited with code %d", exitErr.ExitCode())
		}

		return fmt.Errorf("failed to run editor: %w", runErr)
	}

	return nil
}

func cmdEditor(
	io *IO,
	cfg ticket.Config,
	workDir string,
	args []string,
	env map[string]string,
) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		io.Println("Usage: tk editor <id>")
		io.Println("")
		io.Println("Open a ticket in your preferred editor.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// Check if ticket exists
	if !ticket.Exists(ticketDir, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDir, ticketID)

	// Resolve editor
	editor, resolveErr := resolveEditor(cfg, env)
	if resolveErr != nil {
		return resolveErr
	}

	return runEditor(editor, path)
}
