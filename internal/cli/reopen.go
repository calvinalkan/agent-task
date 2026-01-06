package cli

import (
	"fmt"
	"path/filepath"

	"tk/internal/ticket"
)

const reopenHelp = `  reopen <id>            Set status to open`

func cmdReopen(io *IO, cfg ticket.Config, workDir string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		io.Println("Usage: tk reopen <id>")
		io.Println("")
		io.Println("Set ticket status back to open. Only works on closed tickets.")

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
		return err
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return parseErr
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
	if cacheErr != nil {
		return cacheErr
	}

	io.Println("Reopened", ticketID)

	return nil
}
