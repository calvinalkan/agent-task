package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
)

const unblockHelp = `  unblock <id> <blocker> Remove blocker from ticket`

func cmdUnblock(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk unblock <id> <blocker-id>")
		fprintln(out, "")
		fprintln(out, "Remove a blocker from a ticket's blocked-by list.")

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

	path := TicketPath(ticketDir, ticketID)

	// Use locked operation to atomically check and update blocked-by list
	err := WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current blocked-by list
		blockedBy, readErr := getBlockedByFromContent(content)
		if readErr != nil {
			return nil, readErr
		}

		// Check if actually blocked by this blocker
		idx := slices.Index(blockedBy, blockerID)
		if idx == -1 {
			return nil, fmt.Errorf("%w %s", errNotBlockedBy, blockerID)
		}

		// Remove blocker
		blockedBy = slices.Delete(blockedBy, idx, idx+1)

		// Update the ticket
		return updateBlockedByInContent(content, blockedBy)
	})
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	fprintln(out, "Unblocked", ticketID, "from", blockerID)

	return 0
}
