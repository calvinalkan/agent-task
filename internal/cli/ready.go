package cli

import (
	"context"
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
	return &Command{
		Flags: flag.NewFlagSet("ready", flag.ContinueOnError),
		Usage: "ready",
		Short: "List actionable tickets (unblocked, not closed)",
		Long: `List actionable tickets that can be worked on now.
Shows open tickets with all blockers closed.

Output sorted by priority (P1 first), then by ID.`,
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execReady(io, cfg)
		},
	}
}

func execReady(io *IO, cfg ticket.Config) error {
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

	for _, summary := range ready {
		io.Println(formatReadyLine(summary))
	}

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

			isReady, warnings := checkBlockersResolved(s, statusMap)
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

// checkBlockersResolved checks if a ticket has all its blockers resolved.
func checkBlockersResolved(summary *ticket.Summary, statusMap map[string]string) (bool, []readyWarning) {
	var warnings []readyWarning

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

	return builder.String()
}
