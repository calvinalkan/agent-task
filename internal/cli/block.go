// Package cli implements the command-line interface for tk.
package cli

import (
	"context"
	"fmt"
	"slices"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

// BlockCmd returns the block command.
func BlockCmd(cfg ticket.Config) *Command {
	return &Command{
		Flags: flag.NewFlagSet("block", flag.ContinueOnError),
		Usage: "block <id> <blocker>",
		Short: "Add blocker to ticket",
		Long:  "Add a blocker to a ticket's blocked-by list.",
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execBlock(io, cfg, args)
		},
	}
}

func execBlock(io *IO, cfg ticket.Config, args []string) error {
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

	if !ticket.Exists(cfg.TicketDirAbs, blockerID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, blockerID)
	}

	if ticketID == blockerID {
		return ticket.ErrCannotBlockSelf
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	err := ticket.WithTicketLock(path, func(content []byte) ([]byte, error) {
		blockedBy, readErr := ticket.GetBlockedByFromContent(content)
		if readErr != nil {
			return nil, fmt.Errorf("reading blocked-by: %w", readErr)
		}

		if slices.Contains(blockedBy, blockerID) {
			return nil, fmt.Errorf("%w: %s", ticket.ErrAlreadyBlockedBy, blockerID)
		}

		blockedBy = append(blockedBy, blockerID)

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

	io.Println("Blocked", ticketID, "by", blockerID)

	return nil
}
