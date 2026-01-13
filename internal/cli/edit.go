package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// editStaleTimeout is how long before an edit temp file is considered stale.
const editStaleTimeout = 1 * time.Hour

// EditCmd returns the edit command.
func EditCmd(cfg *ticket.Config, env map[string]string) *Command {
	flags := flag.NewFlagSet("edit", flag.ContinueOnError)
	flagStart := flags.Bool("start", false, "Start editing (write body to temp file)")
	flagApply := flags.Bool("apply", false, "Apply edit (read temp file, update ticket)")
	flagLaunch := flags.BoolP("launch", "l", false, "Open ticket in external editor")

	return &Command{
		Flags: flags,
		Usage: "edit <id> [--start|--apply|--launch]",
		Short: "Edit ticket body",
		Long: `Edit a ticket's body content (title, description, acceptance criteria).

Workflow:
  1. tk edit <id> --start    Copy body to temp file
  2. Edit the temp file
  3. tk edit <id> --apply    Apply changes back to ticket

Or use --launch (-l) to open the full ticket in $EDITOR.`,
		Exec: func(ctx context.Context, io *IO, args []string) error {
			return execEdit(ctx, io, cfg, env, args, *flagStart, *flagApply, *flagLaunch)
		},
	}
}

func execEdit(ctx context.Context, io *IO, cfg *ticket.Config, env map[string]string, args []string, start, apply, launch bool) error {
	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	// Count how many mode flags are set
	modeCount := 0
	if start {
		modeCount++
	}

	if apply {
		modeCount++
	}

	if launch {
		modeCount++
	}

	if modeCount == 0 {
		return ticket.ErrEditModeRequired
	}

	if modeCount > 1 {
		return ticket.ErrEditModesExclusive
	}

	switch {
	case start:
		return execEditStart(io, cfg, env, ticketID)
	case apply:
		return execEditApply(io, cfg, env, ticketID)
	case launch:
		return execEditLaunch(ctx, cfg, env, ticketID)
	}

	return nil
}

func execEditStart(io *IO, cfg *ticket.Config, env map[string]string, ticketID string) error {
	tempPath := editTempPath(env, ticketID)

	// Check if temp file already exists
	info, statErr := os.Stat(tempPath)
	if statErr == nil {
		// File exists - check if stale
		if time.Since(info.ModTime()) > editStaleTimeout {
			return fmt.Errorf("%w (>%s old): delete %s and run --start again",
				ticket.ErrEditStale, editStaleTimeout, tempPath)
		}

		// Fresh edit in progress
		return fmt.Errorf("%w: finish with `tk edit %s --apply` or delete %s to restart",
			ticket.ErrEditInProgress, ticketID, tempPath)
	}

	// Read ticket and extract body
	ticketPath := ticket.Path(cfg.TicketDirAbs, ticketID)

	frontmatter, body, parseErr := parseTicketParts(ticketPath)
	if parseErr != nil {
		return fmt.Errorf("parsing ticket: %w", parseErr)
	}

	// Write body to temp file
	writeErr := os.WriteFile(tempPath, []byte(body), 0o600)
	if writeErr != nil {
		return fmt.Errorf("writing temp file: %w", writeErr)
	}

	// Output frontmatter and instructions
	io.Println(frontmatter)
	io.Println("")
	io.Println("Body copied to: " + tempPath)
	io.Println("Edit the file, then run: tk edit " + ticketID + " --apply")
	io.Println("")
	io.Println("Note: Only body content can be edited, not frontmatter.")

	return nil
}

func execEditApply(io *IO, cfg *ticket.Config, env map[string]string, ticketID string) error {
	tempPath := editTempPath(env, ticketID)

	// Check temp file exists
	info, statErr := os.Stat(tempPath)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return ticket.ErrEditNotStarted
		}

		return fmt.Errorf("checking temp file: %w", statErr)
	}

	// Check if stale
	if time.Since(info.ModTime()) > editStaleTimeout {
		return fmt.Errorf("%w (>%s old): delete %s and run --start again",
			ticket.ErrEditStale, editStaleTimeout, tempPath)
	}

	// Read temp file (new body)
	newBody, readErr := os.ReadFile(tempPath)
	if readErr != nil {
		return fmt.Errorf("reading temp file: %w", readErr)
	}

	// Validate body is not empty and has a heading
	trimmedBody := strings.TrimSpace(string(newBody))
	if trimmedBody == "" {
		return ticket.ErrEditBodyEmpty
	}

	if !hasHeading(trimmedBody) {
		return ticket.ErrEditBodyNoHeading
	}

	// Read original ticket and get frontmatter
	ticketPath := ticket.Path(cfg.TicketDirAbs, ticketID)

	frontmatter, _, parseErr := parseTicketParts(ticketPath)
	if parseErr != nil {
		return fmt.Errorf("parsing ticket: %w", parseErr)
	}

	// Combine frontmatter with new body
	newContent := frontmatter + "\n" + trimmedBody + "\n"

	// Write back to ticket
	writeErr := os.WriteFile(ticketPath, []byte(newContent), 0o600)
	if writeErr != nil {
		return fmt.Errorf("writing ticket: %w", writeErr)
	}

	// Delete temp file
	_ = os.Remove(tempPath)

	io.Println("Updated ticket " + ticketID)

	return nil
}

func execEditLaunch(ctx context.Context, cfg *ticket.Config, env map[string]string, ticketID string) error {
	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	editor, resolveErr := resolveEditor(cfg, env)
	if resolveErr != nil {
		return resolveErr
	}

	return runEditor(ctx, editor, path)
}

// parseTicketParts reads a ticket file and returns the frontmatter (including delimiters)
// and the body (everything after frontmatter) separately.
func parseTicketParts(path string) (string, string, error) {
	file, openErr := os.Open(path)
	if openErr != nil {
		return "", "", fmt.Errorf("open ticket: %w", openErr)
	}

	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)

	var (
		frontmatterLines []string
		bodyLines        []string
	)

	inFrontmatter := false
	frontmatterEnded := false
	frontmatterCount := 0

	for scanner.Scan() {
		line := scanner.Text()

		if !frontmatterEnded {
			if line == "---" {
				frontmatterCount++

				frontmatterLines = append(frontmatterLines, line)

				switch frontmatterCount {
				case 1:
					inFrontmatter = true
				case 2:
					inFrontmatter = false
					frontmatterEnded = true
				}

				continue
			}

			if inFrontmatter {
				frontmatterLines = append(frontmatterLines, line)

				continue
			}
		}

		// After frontmatter, everything is body
		if frontmatterEnded {
			bodyLines = append(bodyLines, line)
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		return "", "", fmt.Errorf("scan ticket: %w", scanErr)
	}

	frontmatter := strings.Join(frontmatterLines, "\n")
	body := strings.Join(bodyLines, "\n")

	// Trim leading empty lines from body but preserve content
	body = strings.TrimLeft(body, "\n")

	return frontmatter, body, nil
}

// hasHeading checks if the content has at least one markdown heading (# ...).
func hasHeading(content string) bool {
	lines := strings.SplitSeq(content, "\n")
	for line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return true
		}
	}

	return false
}

// resolveEditor checks for an available editor using the env map.
// Priority: config.Editor -> $EDITOR -> zed -> vi -> nano -> error.
func resolveEditor(cfg *ticket.Config, env map[string]string) (string, error) {
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

func runEditor(ctx context.Context, editor, path string) error {
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
			return fmt.Errorf("%w: exit code %d", ticket.ErrEditorFailed, exitErr.ExitCode())
		}

		return fmt.Errorf("%w: %w", ticket.ErrEditorFailed, runErr)
	}

	return nil
}

// editTempPath returns the path to the temp file for editing a ticket's body.
// Checks env["TMPDIR"] first (like os.TempDir on Unix), falls back to os.TempDir().
func editTempPath(env map[string]string, ticketID string) string {
	tmpDir := env["TMPDIR"]
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}

	return filepath.Join(tmpDir, fmt.Sprintf("tk-%s.edit.md", ticketID))
}
