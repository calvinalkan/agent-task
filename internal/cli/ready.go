package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"tk/internal/ticket"
)

const readyHelp = `  ready                  List actionable tickets (unblocked, not closed)`

func cmdReady(o *IO, cfg ticket.Config, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printReadyHelp(o)

		return nil
	}

	// List all tickets (need all for blocker resolution)
	results, err := ticket.ListTickets(cfg.TicketDirAbs, ticket.ListTicketsOptions{Limit: 0}, nil)
	if err != nil {
		return err
	}

	// Build status map and filter ready tickets (parallel)
	ready, warnings := filterReadyTickets(results)

	// Collect warnings via IO
	for _, w := range warnings {
		o.WarnLLM(w.issue, w.action)
	}

	// Sort by priority (ascending, P1 first), then by ID
	slices.SortFunc(ready, func(a, b *ticket.Summary) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}

		return strings.Compare(a.ID, b.ID)
	})

	// Output formatted lines
	for _, summary := range ready {
		line := formatReadyLine(summary)
		o.Println(line)
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
// Uses parallel processing for blocker resolution checks.
func filterReadyTickets(results []ticket.Result) ([]*ticket.Summary, []readyWarning) {
	// Build map: ticketID → status (for blocker lookup)
	statusMap := make(map[string]string)

	// Collect valid summaries and parse errors
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

		// Only consider open tickets as candidates
		if result.Summary.Status == ticket.StatusOpen {
			candidates = append(candidates, result.Summary)
		}
	}

	if len(candidates) == 0 {
		return nil, allWarnings
	}

	// Check blockers in parallel
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

	// Collect ready tickets and warnings
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
// A blocker is resolved if it's closed or doesn't exist (missing = resolved with warning).
// Returns (isReady, warnings). Thread-safe: only reads from statusMap.
func checkBlockersResolved(summary *ticket.Summary, statusMap map[string]string) (bool, []readyWarning) {
	var warnings []readyWarning

	for _, blockerID := range summary.BlockedBy {
		status, exists := statusMap[blockerID]

		if !exists {
			// Missing blocker - treat as resolved, collect warning
			warnings = append(warnings, readyWarning{
				issue: fmt.Sprintf("%s blocked by non-existent ticket %s (treating as resolved)",
					summary.ID, blockerID),
				action: "remove the blocker reference or create the missing ticket",
			})

			continue
		}

		if status != ticket.StatusClosed {
			// Blocker is not closed - ticket is not ready
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

func printReadyHelp(o *IO) {
	o.Println("Usage: tk ready")
	o.Println("")
	o.Println("List actionable tickets that can be worked on now.")
	o.Println("Shows open tickets with all blockers closed.")
	o.Println("")
	o.Println("Output sorted by priority (P1 first), then by ID.")
}
