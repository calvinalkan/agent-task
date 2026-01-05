package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"tk/internal/ticket"
)

const closeHelp = `  close <id>             Set status to closed`

func cmdClose(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk close <id>")
		fprintln(out, "")
		fprintln(out, "Set ticket status to closed. Only works on in_progress tickets.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", ticket.ErrIDRequired)

		return 1
	}

	ticketID := args[0]

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// Check if ticket exists
	if !ticket.Exists(ticketDir, ticketID) {
		fprintln(errOut, "error:", ticket.ErrTicketNotFound, ticketID)

		return 1
	}

	path := ticket.Path(ticketDir, ticketID)

	// Use locked operation to atomically check status and update with closed timestamp
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current status
		status, statusErr := ticket.GetStatusFromContent(content)
		if statusErr != nil {
			return nil, fmt.Errorf("reading status: %w", statusErr)
		}

		// Only allow closing in_progress tickets
		if status == ticket.StatusOpen {
			return nil, ticket.ErrTicketNotStarted
		}

		if status == ticket.StatusClosed {
			return nil, ticket.ErrTicketAlreadyClosed
		}

		if status != ticket.StatusInProgress {
			return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotInProgress, status)
		}

		// Update status to closed
		newContent, updateErr := ticket.UpdateStatusInContent(content, ticket.StatusClosed)
		if updateErr != nil {
			return nil, fmt.Errorf("updating status: %w", updateErr)
		}

		// Add closed timestamp
		closedTime := time.Now().UTC().Format(time.RFC3339)

		return ticket.AddFieldToContent(newContent, "closed", closedTime)
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

	fprintln(out, "Closed", ticketID)

	return 0
}
