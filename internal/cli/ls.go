package cli

import (
	"context"
	"encoding/json"
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
	fs.Bool("json", false, "Output as JSON array")

	return &Command{
		Flags: fs,
		Usage: "ls [flags]",
		Short: "List tickets",
		Long:  "List all tickets. Output sorted by ID (oldest first).",
		Exec: func(_ context.Context, io *IO, _ []string) error {
			jsonOutput, _ := fs.GetBool("json")

			return execLs(io, cfg, fs, jsonOutput)
		},
	}
}

var errConflictingFlags = errors.New("--parent and --roots cannot be used together")

func execLs(io *IO, cfg *ticket.Config, fs *flag.FlagSet, jsonOutput bool) error {
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

	if jsonOutput {
		return outputLsJSON(io, valid)
	}

	for _, summary := range valid {
		io.Println(formatTicketLine(summary))
	}

	return nil
}

// lsTicketJSON is the JSON representation of a ticket in ls output.
type lsTicketJSON struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Priority  int      `json:"priority"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Parent    string   `json:"parent,omitempty"`
	BlockedBy []string `json:"blocked_by"`
	Created   string   `json:"created"`
	Closed    string   `json:"closed,omitempty"`
}

func outputLsJSON(io *IO, summaries []*ticket.Summary) error {
	tickets := make([]lsTicketJSON, 0, len(summaries))

	for _, summary := range summaries {
		blockedBy := summary.BlockedBy
		if blockedBy == nil {
			blockedBy = []string{}
		}

		tickets = append(tickets, lsTicketJSON{
			ID:        summary.ID,
			Status:    summary.Status,
			Priority:  summary.Priority,
			Type:      summary.Type,
			Title:     summary.Title,
			Parent:    summary.Parent,
			BlockedBy: blockedBy,
			Created:   summary.Created,
			Closed:    summary.Closed,
		})
	}

	data, err := json.Marshal(tickets)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}

	io.Println(string(data))

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
