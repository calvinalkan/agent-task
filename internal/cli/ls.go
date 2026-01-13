package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

const defaultLimit = 100

// LsCmd returns the ls command.
func LsCmd(cfg *ticket.Config) *Command {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.String("status", "", "Filter by status (open|in_progress|closed)")
	fs.Int("priority", 0, "Filter by priority (1-4)")
	fs.String("type", "", "Filter by type (bug|feature|task|epic|chore)")
	fs.Int("limit", defaultLimit, "Maximum tickets to show")
	fs.Int("offset", 0, "Skip first N tickets")
	fs.String("parent", "", "Filter by parent ticket ID")
	fs.Bool("roots", false, "Show only tickets without a parent")

	return &Command{
		Flags: fs,
		Usage: "ls [flags]",
		Short: "List tickets",
		Long:  "List all tickets. Output sorted by ID (oldest first).",
		Exec: func(_ context.Context, io *IO, _ []string) error {
			return execLs(io, cfg, fs)
		},
	}
}

var errConflictingFlags = errors.New("--parent and --roots cannot be used together")

func execLs(io *IO, cfg *ticket.Config, fs *flag.FlagSet) error {
	status, _ := fs.GetString("status")
	if fs.Changed("status") {
		err := validateStatusFlag(status)
		if err != nil {
			return err
		}
	}

	priority, _ := fs.GetInt("priority")
	if fs.Changed("priority") {
		if priority < 1 || priority > 4 {
			return errors.New("--priority must be 1-4")
		}
	}

	ticketType, _ := fs.GetString("type")
	if fs.Changed("type") {
		if !ticket.IsValidTicketType(ticketType) {
			return fmt.Errorf("invalid type: %s", ticketType)
		}
	}

	limit, _ := fs.GetInt("limit")
	if limit < 0 {
		return errors.New("--limit must be non-negative")
	}

	offset, _ := fs.GetInt("offset")
	if offset < 0 {
		return errors.New("--offset must be non-negative")
	}

	parentFilter, _ := fs.GetString("parent")
	rootsOnly, _ := fs.GetBool("roots")

	if parentFilter != "" && rootsOnly {
		return errConflictingFlags
	}

	listOpts := ticket.ListTicketsOptions{
		Status:    status,
		Priority:  priority,
		Type:      ticketType,
		Parent:    parentFilter,
		RootsOnly: rootsOnly,
		Limit:     limit,
		Offset:    offset,
	}

	results, err := ticket.ListTickets(cfg.TicketDirAbs, &listOpts, nil)
	if err != nil {
		return fmt.Errorf("list tickets: %w", err)
	}

	var valid []*ticket.Summary

	for _, result := range results {
		if result.Err != nil {
			io.WarnLLM(
				fmt.Sprintf("%s: %v", result.Path, result.Err),
				"fix the ticket file or delete it if invalid",
			)

			continue
		}

		valid = append(valid, result.Summary)
	}

	for _, summary := range valid {
		io.Println(formatTicketLine(summary))
	}

	return nil
}

var errInvalidStatus = errors.New("invalid status")

func validateStatusFlag(status string) error {
	if status == "" {
		return fmt.Errorf("%w: (empty)", errInvalidStatus)
	}

	if !isValidStatus(status) {
		return fmt.Errorf("%w: %s", errInvalidStatus, status)
	}

	return nil
}

func isValidStatus(status string) bool {
	return status == ticket.StatusOpen || status == ticket.StatusInProgress || status == ticket.StatusClosed
}

func formatTicketLine(summary *ticket.Summary) string {
	var builder strings.Builder

	builder.WriteString(summary.ID)
	builder.WriteString(" [")
	builder.WriteString(summary.Status)
	builder.WriteString("] - ")
	builder.WriteString(summary.Title)

	if summary.Parent != "" {
		builder.WriteString(" (parent: ")
		builder.WriteString(summary.Parent)
		builder.WriteString(")")
	}

	if len(summary.BlockedBy) > 0 {
		builder.WriteString(" <- blocked-by: [")
		builder.WriteString(strings.Join(summary.BlockedBy, ", "))
		builder.WriteString("]")
	}

	return builder.String()
}
