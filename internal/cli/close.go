package cli

import (
	"fmt"
	"time"

	"tk/internal/ticket"
)

const closeHelp = `  close <id>             Set status to closed`

func cmdClose(o *IO, cfg ticket.Config, ticketDirAbs string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		o.Println("Usage: tk close <id>")
		o.Println("")
		o.Println("Set ticket status to closed. Only works on in_progress tickets.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	// Check if ticket exists
	if !ticket.Exists(ticketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDirAbs, ticketID)

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
		return err
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return parseErr
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDirAbs, ticketID+".md", &summary)
	if cacheErr != nil {
		return cacheErr
	}

	o.Println("Closed", ticketID)

	return nil
}
