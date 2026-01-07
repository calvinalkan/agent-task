package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// CloseCmd returns the close command.
func CloseCmd(cfg ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("close", flag.ContinueOnError),
		Usage: "close <id>",
		Short: "Set status to closed",
		Long:  "Set ticket status to closed. Only works on in_progress tickets.",
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execClose(io, cfg, args)
		},
	}
}

func execClose(io *IO, cfg ticket.Config, args []string) error {
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
			return nil, ticket.ErrTicketNotStarted
		}

		if status == ticket.StatusClosed {
			return nil, ticket.ErrTicketAlreadyClosed
		}

		if status != ticket.StatusInProgress {
			return nil, fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotInProgress, status)
		}

		newContent, updateErr := ticket.UpdateStatusInContent(content, ticket.StatusClosed)
		if updateErr != nil {
			return nil, fmt.Errorf("updating status: %w", updateErr)
		}

		closedTime := time.Now().UTC().Format(time.RFC3339)

		return ticket.AddFieldToContent(newContent, "closed", closedTime)
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

	io.Println("Closed", ticketID)

	return nil
}
