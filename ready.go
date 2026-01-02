package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

const readyHelp = `  ready                  List actionable tickets (unblocked, not closed)`

func cmdReady(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printReadyHelp(out)

		return 0
	}

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// List all tickets
	results, err := ListTickets(ticketDir)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Build status map and filter ready tickets (parallel)
	ready, warnings := filterReadyTickets(results)

	// Output warnings
	hasWarnings := len(warnings) > 0
	for _, w := range warnings {
		fprintln(errOut, w)
	}

	// Sort by priority (ascending, P1 first), then by ID
	slices.SortFunc(ready, func(a, b *TicketSummary) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}

		return strings.Compare(a.ID, b.ID)
	})

	// Output formatted lines
	for _, summary := range ready {
		line := formatReadyLine(summary)
		fprintln(out, line)
	}

	// Return 1 if there were parse errors (warnings)
	if hasWarnings {
		return 1
	}

	return 0
}

// readyCheckResult holds the result of checking if a ticket is ready.
type readyCheckResult struct {
	summary  *TicketSummary
	isReady  bool
	warnings []string
}

// filterReadyTickets builds status map and returns ready tickets.
// Uses parallel processing for blocker resolution checks.
func filterReadyTickets(results []TicketResult) ([]*TicketSummary, []string) {
	// Build map: ticketID → status (for blocker lookup)
	statusMap := make(map[string]string)

	// Collect valid summaries and parse errors
	var candidates []*TicketSummary

	var allWarnings []string

	for _, result := range results {
		if result.Err != nil {
			allWarnings = append(allWarnings, fmt.Sprintf("warning: %s: %v", result.Path, result.Err))

			continue
		}

		statusMap[result.Summary.ID] = result.Summary.Status

		// Only consider open tickets as candidates
		if result.Summary.Status == StatusOpen {
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

		go func(resultIdx int, s *TicketSummary) {
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
	var ready []*TicketSummary

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
func checkBlockersResolved(summary *TicketSummary, statusMap map[string]string) (bool, []string) {
	var warnings []string

	for _, blockerID := range summary.BlockedBy {
		status, exists := statusMap[blockerID]

		if !exists {
			// Missing blocker - treat as resolved, collect warning
			warnings = append(warnings, fmt.Sprintf(
				"warning: %s blocked by non-existent ticket %s (treating as resolved)",
				summary.ID, blockerID))

			continue
		}

		if status != StatusClosed {
			// Blocker is not closed - ticket is not ready
			return false, warnings
		}
	}

	return true, warnings
}

func formatReadyLine(summary *TicketSummary) string {
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

func printReadyHelp(out io.Writer) {
	fprintln(out, "Usage: tk ready")
	fprintln(out, "")
	fprintln(out, "List actionable tickets that can be worked on now.")
	fprintln(out, "Shows open tickets with all blockers closed.")
	fprintln(out, "")
	fprintln(out, "Output sorted by priority (P1 first), then by ID.")
}
