package cli

import (
	"fmt"
	"slices"

	"tk/internal/ticket"
)

const unblockHelp = `  unblock <id> <blocker> Remove blocker from ticket`

func cmdUnblock(o *IO, cfg ticket.Config, ticketDirAbs string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		o.Println("Usage: tk unblock <id> <blocker-id>")
		o.Println("")
		o.Println("Remove a blocker from a ticket's blocked-by list.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	if len(args) < 2 {
		return ticket.ErrBlockerIDRequired
	}

	ticketID := args[0]
	blockerID := args[1]

	// Check if ticket exists
	if !ticket.Exists(ticketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDirAbs, ticketID)

	// Use locked operation to atomically check and update blocked-by list
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current blocked-by list
		blockedBy, readErr := ticket.GetBlockedByFromContent(content)
		if readErr != nil {
			return nil, fmt.Errorf("reading blocked-by: %w", readErr)
		}

		// Check if actually blocked by this blocker
		idx := slices.Index(blockedBy, blockerID)
		if idx == -1 {
			return nil, fmt.Errorf("%w: %s", ticket.ErrNotBlockedBy, blockerID)
		}

		// Remove blocker
		blockedBy = slices.Delete(blockedBy, idx, idx+1)

		// Update the ticket
		return ticket.UpdateBlockedByInContent(content, blockedBy)
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

	o.Println("Unblocked", ticketID, "from", blockerID)

	return nil
}
