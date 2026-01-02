package main

import (
	"fmt"
	"io"
	"path/filepath"
)

const reopenHelp = `  reopen <id>            Set status to open`

//nolint:cyclop,funlen // status validation requires branching
func cmdReopen(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk reopen <id>")
		fprintln(out, "")
		fprintln(out, "Set ticket status back to open. Only works on closed tickets.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", errIDRequired)

		return 1
	}

	ticketID := args[0]

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

	// Use locked operation to atomically check status and update
	err := WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current status
		status, statusErr := getStatusFromContent(content)
		if statusErr != nil {
			return nil, statusErr
		}

		// Only allow reopening closed tickets
		if status == StatusOpen {
			return nil, errTicketAlreadyOpen
		}

		if status == StatusInProgress {
			return nil, errTicketNotClosed
		}

		if status != StatusClosed {
			return nil, fmt.Errorf("%w (current status: %s)", errTicketNotClosed, status)
		}

		// Update status to open
		newContent, updateErr := updateStatusInContent(content, StatusOpen)
		if updateErr != nil {
			return nil, updateErr
		}

		// Remove closed timestamp
		result := removeFieldFromContent(newContent, "closed")
		if result == nil {
			return newContent, nil // field wasn't there, just return status update
		}

		return result, nil
	})
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	fprintln(out, "Reopened", ticketID)

	return 0
}
