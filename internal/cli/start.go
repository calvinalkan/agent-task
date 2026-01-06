package cli

import (
	"fmt"
	"path/filepath"

	"tk/internal/ticket"
)

const startHelp = `  start <id>             Set status to in_progress`

func cmdStart(io *IO, cfg ticket.Config, workDir string, args []string) error {
	if hasHelpFlag(args) {
		io.Println("Usage: tk start <id>")
		io.Println("")
		io.Println("Set ticket status to in_progress. Only works on open tickets.")

		return nil
	}

	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]
	ticketDir := cfg.TicketDir

	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	if !ticket.Exists(ticketDir, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(ticketDir, ticketID)

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

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
	if cacheErr != nil {
		return cacheErr
	}

	io.Println("Started", ticketID)
	io.Println()

	content, err := ticket.ReadTicket(path)
	if err != nil {
		return err
	}

	io.Printf("%s", content)

	return nil
}
