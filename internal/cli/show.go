package cli

import (
	"io"
	"path/filepath"

	"tk/internal/ticket"
)

const showHelp = `  show <id>              Show ticket details`

func cmdShow(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk show <id>")
		fprintln(out, "")
		fprintln(out, "Display the full contents of a ticket.")

		return 0
	}

	if len(args) == 0 {
		fprintln(errOut, "error:", ticket.ErrIDRequired)

		return 1
	}

	ticketID := args[0]

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

	path := ticket.Path(ticketDir, ticketID)

	// Read ticket contents
	content, err := ticket.ReadTicket(path)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Print content without adding extra newline (content already ends with newline)
	_, _ = io.WriteString(out, content)

	return 0
}
