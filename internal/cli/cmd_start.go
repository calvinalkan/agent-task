package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"tk/internal/ticket"
)

const startHelp = `  start <id>             Set status to in_progress`

func cmdStart(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk start <id>")
		fprintln(out, "")
		fprintln(out, "Set ticket status to in_progress. Only works on open tickets.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", ticket.ErrIDRequired)

		return 1
	}

	ticketID := args[0]
	ticketDir := cfg.TicketDir

	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	if !ticket.Exists(ticketDir, ticketID) {
		fprintln(errOut, "error:", ticket.ErrTicketNotFound, ticketID)

		return 1
	}

	path := ticket.Path(ticketDir, ticketID)

	// Use locked operation to atomically check status and update
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		status, statusErr := ticket.GetStatusFromContent(content)
		if statusErr != nil {
			return nil, fmt.Errorf("reading status: %w", statusErr)
		}

		if status != ticket.StatusOpen {
			return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotOpen, status)
		}

		return ticket.UpdateStatusInContent(content, ticket.StatusInProgress)
	})
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
	if cacheErr != nil {
		fprintln(errOut, "error:", cacheErr)

		return 1
	}

	return printStartOutput(out, errOut, ticketID, path)
}

func printStartOutput(out io.Writer, errOut io.Writer, ticketID, path string) int {
	fprintln(out, "Started", ticketID)
	fprintln(out)

	content, err := ticket.ReadTicket(path)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	_, _ = io.WriteString(out, content)

	return 0
}
