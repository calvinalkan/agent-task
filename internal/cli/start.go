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

	status, statusErr := ticket.ReadTicketStatus(path)
	if statusErr != nil {
		return fmt.Errorf("reading status: %w", statusErr)
	}

	if status != ticket.StatusOpen {
		return fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotOpen, status)
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(path)
	if parseErr != nil {
		return fmt.Errorf("parse frontmatter: %w", parseErr)
	}

	// Validate startability against the spec model rules.
	canStartErr := canStartTicket(cfg.TicketDirAbs, &summary)
	if canStartErr != nil {
		return canStartErr
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

	summary, parseErr = ticket.ParseTicketFrontmatter(path)
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

// canStartTicket mirrors the spec model's canStart logic so behavior tests
// stay aligned with the in-memory oracle.
func canStartTicket(ticketDir string, summary *ticket.Summary) error {
	if summary.Status != ticket.StatusOpen {
		return fmt.Errorf("%w (current status: %s)", ticket.ErrTicketNotOpen, summary.Status)
	}

	for _, blockerID := range summary.BlockedBy {
		blocker, err := readSummary(ticketDir, blockerID, "blocker")
		if err != nil {
			return err
		}

		if blocker.Status != ticket.StatusClosed {
			return fmt.Errorf("blocked by open blocker: %s", blockerID)
		}
	}

	if summary.Parent == "" {
		return nil
	}

	parent, err := readSummary(ticketDir, summary.Parent, "parent")
	if err != nil {
		return err
	}

	if parent.Status == ticket.StatusOpen {
		return fmt.Errorf("%w: %s", ticket.ErrParentNotStarted, summary.Parent)
	}

	ancestorErr := ensureAncestorUnblocked(ticketDir, parent, make(map[string]bool))
	if ancestorErr != nil {
		return fmt.Errorf("ancestor not ready: %s: %w", parent.ID, ancestorErr)
	}

	return nil
}

func ensureAncestorUnblocked(ticketDir string, summary *ticket.Summary, visited map[string]bool) error {
	if visited[summary.ID] {
		return fmt.Errorf("ancestor cycle detected: %s", summary.ID)
	}

	visited[summary.ID] = true

	for _, blockerID := range summary.BlockedBy {
		blocker, err := readSummary(ticketDir, blockerID, "blocker")
		if err != nil {
			return err
		}

		if blocker.Status != ticket.StatusClosed {
			return fmt.Errorf("blocked by open blocker: %s", blockerID)
		}
	}

	if summary.Parent == "" {
		return nil
	}

	parent, err := readSummary(ticketDir, summary.Parent, "parent")
	if err != nil {
		return err
	}

	return ensureAncestorUnblocked(ticketDir, parent, visited)
}

func readSummary(ticketDir, id, relation string) (*ticket.Summary, error) {
	if !ticket.Exists(ticketDir, id) {
		switch relation {
		case "parent":
			return nil, fmt.Errorf("%w: %s", ticket.ErrParentNotFound, id)
		case "blocker":
			return nil, fmt.Errorf("blocker not found: %s", id)
		default:
			return nil, fmt.Errorf("%w: %s", ticket.ErrTicketNotFound, id)
		}
	}

	path := ticket.Path(ticketDir, id)

	summary, err := ticket.ParseTicketFrontmatter(path)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	return &summary, nil
}
