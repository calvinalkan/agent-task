package cli

import (
	"context"
	"fmt"
	"slices"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

// UnblockCmd returns the unblock command.
func UnblockCmd(cfg ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("unblock", flag.ContinueOnError),
		Usage: "unblock <id> <blocker>",
		Short: "Remove blocker from ticket",
		Long:  "Remove a blocker from a ticket's blocked-by list.",
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execUnblock(io, cfg, args)
		},
	}
}

func execUnblock(io *IO, cfg ticket.Config, args []string) error {
	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	if len(args) < 2 {
		return ticket.ErrBlockerIDRequired
	}

	ticketID := args[0]
	blockerID := args[1]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		blockedBy, readErr := ticket.GetBlockedByFromContent(content)
		if readErr != nil {
			return nil, fmt.Errorf("reading blocked-by: %w", readErr)
		}

		idx := slices.Index(blockedBy, blockerID)
		if idx == -1 {
			return nil, fmt.Errorf("%w: %s", ticket.ErrNotBlockedBy, blockerID)
		}

		blockedBy = slices.Delete(blockedBy, idx, idx+1)

		return ticket.UpdateBlockedByInContent(content, blockedBy)
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

	io.Println("Unblocked", ticketID, "from", blockerID)

	return nil
}
