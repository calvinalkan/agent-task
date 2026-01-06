package cli

import (
	"fmt"

	"tk/internal/ticket"
)

const startHelp = `  start <id>             Set status to in_progress`

func cmdStart(o *IO, cfg ticket.Config, args []string) error {
	if hasHelpFlag(args) {
		o.Println("Usage: tk start <id>")
		o.Println("")
		o.Println("Set ticket status to in_progress. Only works on open tickets.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	// Use locked operation to atomically check status and update
	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		status, statusErr := ticket.GetStatusFromContent(content)
		if statusErr != nil {
			return nil, fmt.Errorf("reading status: %w", statusErr)
		}

		if status != ticket.StatusOpen {
			return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotOpen, status)
		}

		return ticket.UpdateStatusInContent(content, ticket.StatusInProgress)
	})
	if err != nil {
		return err
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return parseErr
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(cfg.TicketDirAbs, ticketID+".md", &summary)
	if cacheErr != nil {
		return cacheErr
	}

	o.Println("Started", ticketID)
	o.Println()

	content, err := ticket.ReadTicket(path)
	if err != nil {
		return err
	}

	o.Printf("%s", content)

	return nil
}
