package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"
)

const closeHelp = `  close <id>             Set status to closed`

//nolint:cyclop,funlen // status validation requires branching
func cmdClose(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk close <id>")
		fprintln(out, "")
		fprintln(out, "Set ticket status to closed. Only works on in_progress tickets.")

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

	// Use locked operation to atomically check status and update with closed timestamp
	err := WithTicketLock(path, func(content []byte) ([]byte, error) {
		// Read current status
		status, statusErr := getStatusFromContent(content)
		if statusErr != nil {
			return nil, statusErr
		}

		// Only allow closing in_progress tickets
		if status == StatusOpen {
			return nil, errTicketNotStarted
		}

		if status == StatusClosed {
			return nil, errTicketAlreadyClosed
		}

		if status != StatusInProgress {
			return nil, fmt.Errorf("%w (current status: %s)", errTicketNotInProgress, status)
		}

		// Update status to closed
		newContent, updateErr := updateStatusInContent(content, StatusClosed)
		if updateErr != nil {
			return nil, updateErr
		}

		// Add closed timestamp
		closedTime := time.Now().UTC().Format(time.RFC3339)

		return addFieldToContent(newContent, "closed", closedTime)
	})
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	fprintln(out, "Closed", ticketID)

	return 0
}
