package cli

import (
	"context"
	"fmt"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// ReopenCmd returns the reopen command.
func ReopenCmd(cfg ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("reopen", flag.ContinueOnError),
		Usage: "reopen <id>",
		Short: "Set status to open",
		Long: `Set ticket status back to open.

Requirements:
  - Ticket must be closed
  - Parent ticket must not be closed (reopen parent first)`,
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

	// Check parent constraints before acquiring lock
	parentID, parentErr := ticket.ReadTicketParent(path)
	if parentErr != nil {
		return fmt.Errorf("reading parent: %w", parentErr)
	}

	if parentID != "" {
		parentPath := ticket.Path(cfg.TicketDirAbs, parentID)

		parentStatus, statusErr := ticket.ReadTicketStatus(parentPath)
		if statusErr != nil {
			return fmt.Errorf("reading parent status: %w", statusErr)
		}

		if parentStatus == ticket.StatusClosed {
			return fmt.Errorf("%w: %s", ticket.ErrParentClosed, parentID)
		}
	}

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
