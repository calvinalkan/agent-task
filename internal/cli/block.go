// Package cli implements the command-line interface for tk.
package cli

import (
	"fmt"
	"slices"

	"tk/internal/ticket"
)

const blockHelp = `  block <id> <blocker>   Add blocker to ticket`

func cmdBlock(o *IO, cfg ticket.Config, ticketDirAbs string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		o.Println("Usage: tk block <id> <blocker-id>")
		o.Println("")
		o.Println("Add a blocker to a ticket's blocked-by list.")

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

	// Check if blocker ticket exists
	if !ticket.Exists(ticketDirAbs, blockerID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, blockerID)
	}

	// Cannot block self
	if ticketID == blockerID {
		return ticket.ErrCannotBlockSelf
	}

	path := ticket.Path(ticketDirAbs, ticketID)

	// Use locked operation to atomically check and update blocked-by list
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current blocked-by list
		blockedBy, readErr := ticket.GetBlockedByFromContent(content)
		if readErr != nil {
			return nil, fmt.Errorf("reading blocked-by: %w", readErr)
		}

		// Check if already blocked by this blocker
		if slices.Contains(blockedBy, blockerID) {
			return nil, fmt.Errorf("%w: %s", ticket.ErrAlreadyBlockedBy, blockerID)
		}

		// Add blocker
		blockedBy = append(blockedBy, blockerID)

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

	o.Println("Blocked", ticketID, "by", blockerID)

	return nil
}
