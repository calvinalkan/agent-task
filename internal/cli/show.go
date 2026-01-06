package cli

import (
	"fmt"
	"path/filepath"

	"tk/internal/ticket"
)

const showHelp = `  show <id>              Show ticket details`

func cmdShow(io *IO, cfg ticket.Config, workDir string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		io.Println("Usage: tk show <id>")
		io.Println("")
		io.Println("Display the full contents of a ticket.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// Check if ticket exists
	if !ticket.Exists(ticketDir, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDir, ticketID)

	// Read ticket contents
	content, err := ticket.ReadTicket(path)
	if err != nil {
		return err
	}

	// Print content (Printf to avoid extra newline since content already ends with newline)
	io.Printf("%s", content)

	return nil
}
