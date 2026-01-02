package main

import (
	"io"
	"path/filepath"
)

const showHelp = `  show <id>              Show ticket details`

func cmdShow(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk show <id>")
		fprintln(out, "")
		fprintln(out, "Display the full contents of a ticket.")

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

	// Read ticket contents
	content, err := ReadTicket(path)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Print content without adding extra newline (content already ends with newline)
	_, _ = io.WriteString(out, content)

	return 0
}
