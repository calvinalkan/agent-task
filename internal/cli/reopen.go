package cli

import (
	"context"
	"fmt"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

// ReopenCmd returns the reopen command.
func ReopenCmd(cfg ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("reopen", flag.ContinueOnError),
		Usage: "reopen <id>",
		Short: "Set status to open",
		Long:  "Set ticket status back to open. Only works on closed tickets.",
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execReopen(io, cfg, args)
		},
	}
}

func execReopen(io *IO, cfg ticket.Config, args []string) error {
	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		status, statusErr := ticket.GetStatusFromContent(content)
		if statusErr != nil {
			return nil, fmt.Errorf("reading status: %w", statusErr)
		}

		if status == ticket.StatusOpen {
			return nil, ticket.ErrTicketAlreadyOpen
		}

		if status == ticket.StatusInProgress {
			return nil, ticket.ErrTicketNotClosed
		}

		if status != ticket.StatusClosed {
			return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotClosed, status)
		}

		newContent, updateErr := ticket.UpdateStatusInContent(content, ticket.StatusOpen)
		if updateErr != nil {
			return nil, fmt.Errorf("updating status: %w", updateErr)
		}

		result := ticket.RemoveFieldFromContent(newContent, "closed")
		if result == nil {
			return newContent, nil
		}

		return result, nil
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

	io.Println("Reopened", ticketID)

	return nil
}
