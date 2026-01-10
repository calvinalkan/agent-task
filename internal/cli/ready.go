package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

// ReadyCmd returns the ready command.
func ReadyCmd(cfg ticket.Config) *Command {
	fs := flag.NewFlagSet("ready", flag.ContinueOnError)
	fs.Bool("json", false, "Output as JSON array")
	fs.Int("limit", 0, "Maximum tickets to show (0 = no limit)")
	fs.String("field", "", "Output only this field (id|priority|status|type|title|parent|created)")

	return &Command{
		Flags: fs,
		Usage: "ready [flags]",
		Short: "List actionable tickets (unblocked, not closed)",
		Long: `List actionable tickets that can be worked on now.

A ticket is ready if:
  - Status is open
  - All blockers are closed
  - Parent is started (in_progress) or no parent

Output sorted by priority (P1 first), then by ID.

Examples:
  tk ready                          # List all ready tickets
  tk ready --limit 1                # Show only the top priority ticket
  tk ready --field id --limit 1     # Get just the ID of top ticket
  tk ready --json                   # Output as JSON array
  tk ready --json --field id        # JSON array of IDs: ["id1", "id2"]

  # Start the highest priority ready ticket:
  tk start $(tk ready --field id --limit 1)`,
		Exec: func(_ context.Context, io *IO, args []string) error {
			jsonOutput, _ := fs.GetBool("json")
			limit, _ := fs.GetInt("limit")
			field, _ := fs.GetString("field")
			return execReady(io, cfg, jsonOutput, limit, field)
		},
	}
}

var errInvalidField = fmt.Errorf("invalid field (valid: id, priority, status, type, title, parent, created)")

func execReady(io *IO, cfg ticket.Config, jsonOutput bool, limit int, field string) error {
	if field != "" && !isValidReadyField(field) {
		return errInvalidField
	}

	results, err := ticket.ListTickets(cfg.TicketDirAbs, ticket.ListTicketsOptions{Limit: 0}, nil)
	if err != nil {
		return err
	}

	ready, warnings := filterReadyTickets(results)

	for _, w := range warnings {
		io.WarnLLM(w.issue, w.action)
	}

	slices.SortFunc(ready, func(a, b *ticket.Summary) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}

		return strings.Compare(a.ID, b.ID)
	})

	if limit > 0 && len(ready) > limit {
		ready = ready[:limit]
	}

	if jsonOutput {
		if field != "" {
			return outputReadyFieldJSON(io, ready, field)
		}

		return outputReadyJSON(io, ready)
	}

	if len(ready) == 0 {
		io.ErrPrintln("no tickets ready for pickup")

		return nil
	}

	if field != "" {
		for _, summary := range ready {
			io.Println(getFieldValue(summary, field))
		}

		return nil
	}

	for _, summary := range ready {
		io.Println(formatReadyLine(summary))
	}

	return nil
}

func isValidReadyField(field string) bool {
	switch field {
	case "id", "priority", "status", "type", "title", "parent", "created":
		return true
	default:
		return false
	}
}

func getFieldValue(s *ticket.Summary, field string) string {
	switch field {
	case "id":
		return s.ID
	case "priority":
		return strconv.Itoa(s.Priority)
	case "status":
		return s.Status
	case "type":
		return s.Type
	case "title":
		return s.Title
	case "parent":
		return s.Parent
	case "created":
		return s.Created
	default:
		return ""
	}
}

func outputReadyFieldJSON(io *IO, ready []*ticket.Summary, field string) error {
	values := make([]any, 0, len(ready))

	for _, s := range ready {
		switch field {
		case "priority":
			values = append(values, s.Priority)
		default:
			values = append(values, getFieldValue(s, field))
		}
	}

	data, err := json.Marshal(values)
	if err != nil {
		return err
	}

	io.Println(string(data))

	return nil
}

// readyTicketJSON is the JSON representation of a ready ticket.
type readyTicketJSON struct {
	ID        string   `json:"id"`
	Priority  int      `json:"priority"`
	Status    string   `json:"status"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Parent    string   `json:"parent,omitempty"`
	BlockedBy []string `json:"blocked_by"`
	Created   string   `json:"created"`
}

func outputReadyJSON(io *IO, ready []*ticket.Summary) error {
	tickets := make([]readyTicketJSON, 0, len(ready))

	for _, s := range ready {
		blockedBy := s.BlockedBy
		if blockedBy == nil {
			blockedBy = []string{}
		}

		tickets = append(tickets, readyTicketJSON{
			ID:        s.ID,
			Priority:  s.Priority,
			Status:    s.Status,
			Type:      s.Type,
			Title:     s.Title,
			Parent:    s.Parent,
			BlockedBy: blockedBy,
			Created:   s.Created,
		})
	}

	data, err := json.Marshal(tickets)
	if err != nil {
		return err
	}

	io.Println(string(data))

	return nil
}

// readyWarning holds a warning with issue and action for WarnLLM.
type readyWarning struct {
	issue  string
	action string
}

// readyCheckResult holds the result of checking if a ticket is ready.
type readyCheckResult struct {
	summary  *ticket.Summary
	isReady  bool
	warnings []readyWarning
}

// filterReadyTickets builds status map and returns ready tickets.
func filterReadyTickets(results []ticket.Result) ([]*ticket.Summary, []readyWarning) {
	statusMap := make(map[string]string)

	var candidates []*ticket.Summary

	var allWarnings []readyWarning

	for _, result := range results {
		if result.Err != nil {
			allWarnings = append(allWarnings, readyWarning{
				issue:  fmt.Sprintf("%s: %v", result.Path, result.Err),
				action: "fix the ticket file or delete it if invalid",
			})

			continue
		}

		statusMap[result.Summary.ID] = result.Summary.Status

		if result.Summary.Status == ticket.StatusOpen {
			candidates = append(candidates, result.Summary)
		}
	}

	if len(candidates) == 0 {
		return nil, allWarnings
	}

	checkResults := make([]readyCheckResult, len(candidates))

	var waitGroup sync.WaitGroup

	for idx, summary := range candidates {
		waitGroup.Add(1)

		go func(resultIdx int, s *ticket.Summary) {
			defer waitGroup.Done()

			isReady, warnings := checkTicketReady(s, statusMap)
			checkResults[resultIdx] = readyCheckResult{
				summary:  s,
				isReady:  isReady,
				warnings: warnings,
			}
		}(idx, summary)
	}

	waitGroup.Wait()

	var ready []*ticket.Summary

	for _, r := range checkResults {
		allWarnings = append(allWarnings, r.warnings...)

		if r.isReady {
			ready = append(ready, r.summary)
		}
	}

	return ready, allWarnings
}

// checkTicketReady checks if a ticket can be started (blockers resolved, parent started).
func checkTicketReady(summary *ticket.Summary, statusMap map[string]string) (bool, []readyWarning) {
	var warnings []readyWarning

	// Check blockers are all closed
	for _, blockerID := range summary.BlockedBy {
		status, exists := statusMap[blockerID]

		if !exists {
			warnings = append(warnings, readyWarning{
				issue: fmt.Sprintf("%s blocked by non-existent ticket %s (treating as resolved)",
					summary.ID, blockerID),
				action: "remove the blocker reference or create the missing ticket",
			})

			continue
		}

		if status != ticket.StatusClosed {
			return false, warnings
		}
	}

	// Check parent is started (not open)
	if summary.Parent != "" {
		parentStatus, exists := statusMap[summary.Parent]

		if !exists {
			warnings = append(warnings, readyWarning{
				issue: fmt.Sprintf("%s has non-existent parent %s (treating as no parent)",
					summary.ID, summary.Parent),
				action: "remove the parent reference or create the missing ticket",
			})
		} else if parentStatus != ticket.StatusInProgress {
			// Parent must be in_progress for child to be startable
			// (Parent can't be closed while child is open - we enforce this)
			return false, warnings
		}
	}

	return true, warnings
}

func formatReadyLine(summary *ticket.Summary) string {
	var builder strings.Builder

	builder.WriteString(summary.ID)
	builder.WriteString("  [P")
	builder.WriteString(strconv.Itoa(summary.Priority))
	builder.WriteString("][")
	builder.WriteString(summary.Status)
	builder.WriteString("] - ")
	builder.WriteString(summary.Title)

	if summary.Parent != "" {
		builder.WriteString(" (parent: ")
		builder.WriteString(summary.Parent)
		builder.WriteString(")")
	}

	return builder.String()
}
