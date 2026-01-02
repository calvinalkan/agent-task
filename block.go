package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
)

const blockHelp = `  block <id> <blocker>   Add blocker to ticket`

//nolint:cyclop,funlen // validation requires branching
func cmdBlock(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk block <id> <blocker-id>")
		fprintln(out, "")
		fprintln(out, "Add a blocker to a ticket's blocked-by list.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", errIDRequired)

		return 1
	}

	if len(args) < 2 { //nolint:mnd // need exactly 2 args
		fprintln(errOut, "error:", errBlockerIDRequired)

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
	if !TicketExists(ticketDir, ticketID) {
		fprintln(errOut, "error:", errTicketNotFound, ticketID)

		return 1
	}

	// Check if blocker ticket exists
	if !TicketExists(ticketDir, blockerID) {
		fprintln(errOut, "error:", errTicketNotFound, blockerID)

		return 1
	}

	// Cannot block self
	if ticketID == blockerID {
		fprintln(errOut, "error:", errCannotBlockSelf)

		return 1
	}

	path := TicketPath(ticketDir, ticketID)

	// Use locked operation to atomically check and update blocked-by list
	err := WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current blocked-by list
		blockedBy, readErr := getBlockedByFromContent(content)
		if readErr != nil {
			return nil, readErr
		}

		// Check if already blocked by this blocker
		if slices.Contains(blockedBy, blockerID) {
			return nil, fmt.Errorf("%w %s", errAlreadyBlockedBy, blockerID)
		}

		// Add blocker
		blockedBy = append(blockedBy, blockerID)

		// Update the ticket
		return updateBlockedByInContent(content, blockedBy)
	})
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	fprintln(out, "Blocked", ticketID, "by", blockerID)

	return 0
}
