package cli

import (
	"fmt"

	"tk/internal/ticket"
)

const showHelp = `  show <id>              Show ticket details`

func cmdShow(o *IO, cfg ticket.Config, ticketDirAbs string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		o.Println("Usage: tk show <id>")
		o.Println("")
		o.Println("Display the full contents of a ticket.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	// Check if ticket exists
	if !ticket.Exists(ticketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDirAbs, ticketID)

	// Read ticket contents
	content, err := ticket.ReadTicket(path)
	if err != nil {
		return err
	}

	// Print content (Printf to avoid extra newline since content already ends with newline)
	o.Printf("%s", content)

	return nil
}
