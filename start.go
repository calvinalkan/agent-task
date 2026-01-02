package main

import (
	"fmt"
	"io"
	"path/filepath"
)

const startHelp = `  start <id>             Set status to in_progress`

func cmdStart(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk start <id>")
		fprintln(out, "")
		fprintln(out, "Set ticket status to in_progress. Only works on open tickets.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", errIDRequired)

		return 1
	}

	ticketID := args[0]
	ticketDir := cfg.TicketDir

	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	if !TicketExists(ticketDir, ticketID) {
		fprintln(errOut, "error:", errTicketNotFound, ticketID)

		return 1
	}

	path := TicketPath(ticketDir, ticketID)

	// Use locked operation to atomically check status and update
	err := WithTicketLock(path, func(content []byte) ([]byte, error) {
		status, statusErr := getStatusFromContent(content)
		if statusErr != nil {
			return nil, statusErr
		}

		if status != StatusOpen {
			return nil, fmt.Errorf("%w (current status: %s)", errTicketNotOpen, status)
		}

		return updateStatusInContent(content, StatusInProgress)
	})
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	return printStartOutput(out, errOut, ticketID, path)
}

func printStartOutput(out io.Writer, errOut io.Writer, ticketID, path string) int {
	fprintln(out, "Started", ticketID)
	fprintln(out)

	content, err := ReadTicket(path)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	_, _ = io.WriteString(out, content)

	return 0
}
