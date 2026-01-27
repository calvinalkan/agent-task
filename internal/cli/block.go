// Package cli implements the command-line interface for tk.
package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// BlockCmd returns the block command.
func BlockCmd(cfg *ticket.Config) *Command {
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

func execBlock(io *IO, cfg *ticket.Config, args []string) error {
	if len(args) == 0 {
		return ticket.ErrIDRequired
	}

	ticketID := args[0]

	if !ticket.Exists(cfg.TicketDirAbs, ticketID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, ticketID)
	}

	path := ticket.Path(cfg.TicketDirAbs, ticketID)

	status, statusErr := ticket.ReadTicketStatus(path)
	if statusErr != nil {
		return fmt.Errorf("reading status: %w", statusErr)
	}

	if status == ticket.StatusClosed {
		return ticket.ErrTicketAlreadyClosed
	}

	if len(args) < 2 {
		return ticket.ErrBlockerIDRequired
	}

	blockerID := args[1]

	if blockerID == "" {
		return ticket.ErrBlockerIDRequired
	}

	if !ticket.Exists(cfg.TicketDirAbs, blockerID) {
		return fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, blockerID)
	}

	if ticketID == blockerID {
		return ticket.ErrCannotBlockSelf
	}

	cycle, cycleErr := blockerCyclePath(cfg.TicketDirAbs, ticketID, blockerID)
	if cycleErr != nil {
		return fmt.Errorf("check blocker cycle: %w", cycleErr)
	}

	if cycle != nil {
		// Keep the error message aligned with the spec model for behavior tests.
		return fmt.Errorf("blocker cycle detected: %s", formatCyclePath(cycle))
	}

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

	io.Println("Blocked", ticketID, "by", blockerID)

	return nil
}

// blockerCyclePath returns the cycle path if adding "ticketID blocked by blockerID"
// would create a cycle. This mirrors the spec model so behavior tests stay aligned.
func blockerCyclePath(ticketDir, ticketID, blockerID string) ([]string, error) {
	visited := make(map[string]bool)

	path, err := findBlockerPath(ticketDir, blockerID, ticketID, visited)
	if err != nil {
		return nil, err
	}

	if path == nil {
		return nil, nil
	}

	return append([]string{ticketID}, path...), nil
}

func findBlockerPath(ticketDir, from, target string, visited map[string]bool) ([]string, error) {
	if visited[from] {
		return nil, nil
	}

	visited[from] = true

	if from == target {
		return []string{target}, nil
	}

	blockedBy, err := ticket.ReadTicketBlockedBy(ticket.Path(ticketDir, from))
	if err != nil {
		return nil, fmt.Errorf("reading blocked-by for %s: %w", from, err)
	}

	for _, blockerID := range blockedBy {
		path, pathErr := findBlockerPath(ticketDir, blockerID, target, visited)
		if pathErr != nil {
			return nil, pathErr
		}

		if path != nil {
			return append([]string{from}, path...), nil
		}
	}

	return nil, nil
}

func formatCyclePath(path []string) string {
	return strings.Join(path, " -> ")
}
