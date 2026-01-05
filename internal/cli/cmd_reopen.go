package cli

import (
	"fmt"
	"io"
	"path/filepath"

	"tk/internal/ticket"
)

const reopenHelp = `  reopen <id>            Set status to open`

func cmdReopen(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk reopen <id>")
		fprintln(out, "")
		fprintln(out, "Set ticket status back to open. Only works on closed tickets.")

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

	// Use locked operation to atomically check status and update
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current status
		status, statusErr := ticket.GetStatusFromContent(content)
		if statusErr != nil {
			return nil, fmt.Errorf("reading status: %w", statusErr)
		}

		// Only allow reopening closed tickets
		if status == ticket.StatusOpen {
			return nil, ticket.ErrTicketAlreadyOpen
		}

		if status == ticket.StatusInProgress {
			return nil, ticket.ErrTicketNotClosed
		}

		if status != ticket.StatusClosed {
			return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotClosed, status)
		}

		// Update status to open
		newContent, updateErr := ticket.UpdateStatusInContent(content, ticket.StatusOpen)
		if updateErr != nil {
			return nil, fmt.Errorf("updating status: %w", updateErr)
		}

		// Remove closed timestamp
		result := ticket.RemoveFieldFromContent(newContent, "closed")
		if result == nil {
			return newContent, nil // field wasn't there, just return status update
		}

		return result, nil
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

	fprintln(out, "Reopened", ticketID)

	return 0
}
