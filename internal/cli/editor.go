package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// resolveEditor checks for an available editor using the env map.
// Priority: config.Editor -> $EDITOR -> zed -> vi -> nano -> error.
func resolveEditor(cfg ticket.Config, env map[string]string) (string, error) {
	if cfg.Editor != "" {
		_, lookErr := exec.LookPath(cfg.Editor)
		if lookErr == nil {
			return cfg.Editor, nil
		}
	}

	if editor := env["EDITOR"]; editor != "" {
		_, lookErr := exec.LookPath(editor)
		if lookErr == nil {
			return editor, nil
		}
	}

	_, zedErr := exec.LookPath("zed")
	if zedErr == nil {
		return "zed", nil
	}

	_, viErr := exec.LookPath("vi")
	if viErr == nil {
		return "vi", nil
	}

	_, nanoErr := exec.LookPath("nano")
	if nanoErr == nil {
		return "nano", nil
	}

	return "", ticket.ErrNoEditorFound
}

func runEditor(editor, path string) error {
	ctx := context.Background()

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

// EditorCmd returns the editor command.
func EditorCmd(cfg ticket.Config, env map[string]string) *Command {
	return &Command{
		Flags: flag.NewFlagSet("editor", flag.ContinueOnError),
		Usage: "editor <id>",
		Short: "Open ticket in editor",
		Long:  "Open a ticket in your preferred editor.",
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execEditor(io, cfg, env, args)
		},
	}
}

func execEditor(io *IO, cfg ticket.Config, env map[string]string, args []string) error {
	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	editor, resolveErr := resolveEditor(cfg, env)
	if resolveErr != nil {
		return resolveErr
	}

	return runEditor(editor, path)
}
