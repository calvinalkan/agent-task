// Package cli implements the command-line interface for tk.
package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"

	"tk/internal/ticket"
)

const blockHelp = `  block <id> <blocker>   Add blocker to ticket`

func cmdBlock(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk block <id> <blocker-id>")
		fprintln(out, "")
		fprintln(out, "Add a blocker to a ticket's blocked-by list.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", ticket.ErrIDRequired)

		return 1
	}

	if len(args) < 2 {
		fprintln(errOut, "error:", ticket.ErrBlockerIDRequired)

		return 1
	}

	ticketID := args[0]
	blockerID := args[1]

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

	// Check if blocker ticket exists
	if !ticket.Exists(ticketDir, blockerID) {
		fprintln(errOut, "error:", ticket.ErrTicketNotFound, blockerID)

		return 1
	}

	// Cannot block self
	if ticketID == blockerID {
		fprintln(errOut, "error:", ticket.ErrCannotBlockSelf)

		return 1
	}

	path := ticket.Path(ticketDir, ticketID)

	// Use locked operation to atomically check and update blocked-by list
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current blocked-by list
		blockedBy, readErr := ticket.GetBlockedByFromContent(content)
		if readErr != nil {
			return nil, fmt.Errorf("reading blocked-by: %w", readErr)
		}

		// Check if already blocked by this blocker
		if slices.Contains(blockedBy, blockerID) {
			return nil, fmt.Errorf("%w %s", ticket.ErrAlreadyBlockedBy, blockerID)
		}

		// Add blocker
		blockedBy = append(blockedBy, blockerID)

		// Update the ticket
		return ticket.UpdateBlockedByInContent(content, blockedBy)
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

	fprintln(out, "Blocked", ticketID, "by", blockerID)

	return 0
}
