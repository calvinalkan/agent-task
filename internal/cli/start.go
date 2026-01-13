package cli

import (
	"context"
	"fmt"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// StartCmd returns the start command.
func StartCmd(cfg *ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("start", flag.ContinueOnError),
		Usage: "start <id>",
		Short: "Set status to in_progress",
		Long: `Set ticket status to in_progress.

Requirements:
  - Ticket must be open
  - Parent ticket must be started first (if any)`,
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execStart(io, cfg, args)
		},
	}
}

func execStart(io *IO, cfg *ticket.Config, args []string) error {
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

		if parentStatus == ticket.StatusOpen {
			return fmt.Errorf("%w: %s", ticket.ErrParentNotStarted, parentID)
		}
	}

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
		return fmt.Errorf("update ticket: %w", err)
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return fmt.Errorf("parse frontmatter: %w", parseErr)
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(cfg.TicketDirAbs, ticketID+".md", &summary)
	if cacheErr != nil {
		return fmt.Errorf("update cache: %w", cacheErr)
	}

	io.Println("Started", ticketID)
	io.Println()

	content, err := ticket.ReadTicket(path)
	if err != nil {
		return fmt.Errorf("read ticket: %w", err)
	}

	io.Printf("%s", content)

	return nil
}
