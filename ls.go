package main

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	flag "github.com/spf13/pflag"
)

const defaultLimit = 100

func cmdLs(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printLsHelp(out)

		return 0
	}

	flagSet := flag.NewFlagSet("ls", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	status := flagSet.String("status", "", "Filter by status")
	limit := flagSet.Int("limit", defaultLimit, "Maximum tickets to show")
	offset := flagSet.Int("offset", 0, "Skip first N tickets")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	// Validate status if provided
	if flagSet.Changed("status") {
		validateErr := validateStatusFlag(*status)
		if validateErr != nil {
			fprintln(errOut, "error:", validateErr)

			return 1
		}
	}

	// Validate limit
	if *limit < 0 {
		fprintln(errOut, "error: --limit must be non-negative")

		return 1
	}

	// Validate offset
	if *offset < 0 {
		fprintln(errOut, "error: --offset must be non-negative")

		return 1
	}

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// List tickets
	results, err := ListTickets(ticketDir)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Output tickets and track errors
	hasErrors := outputTickets(out, errOut, results, *status, *limit, *offset)

	if hasErrors {
		return 1
	}

	return 0
}

func printLsHelp(out io.Writer) {
	fprintln(out, "Usage: tk ls [options]")
	fprintln(out, "")
	fprintln(out, "List all tickets. Output sorted by ID (oldest first).")
	fprintln(out, "")
	fprintln(out, "Options:")
	fprintln(out, "  --status=<status>    Filter by status (open|in_progress|closed)")
	fprintln(out, "  --limit=N            Max tickets to show [default: 100]")
	fprintln(out, "  --offset=N           Skip first N tickets [default: 0]")
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

func outputTickets(
	out io.Writer,
	errOut io.Writer,
	results []TicketResult,
	statusFilter string,
	limit, offset int,
) bool {
	hasErrors := false

	// First pass: filter by status and collect errors
	filtered := make([]*TicketSummary, 0, len(results))

	for _, result := range results {
		if result.Err != nil {
			fprintln(errOut, "warning:", result.Path+":", result.Err)

			hasErrors = true

			continue
		}

		// Apply status filter
		if statusFilter != "" && result.Summary.Status != statusFilter {
			continue
		}

		filtered = append(filtered, result.Summary)
	}

	total := len(filtered)

	// Apply offset
	if offset >= total {
		filtered = nil
	} else {
		filtered = filtered[offset:]
	}

	// Apply limit
	truncated := false
	remaining := 0

	if limit < len(filtered) {
		remaining = len(filtered) - limit
		filtered = filtered[:limit]
		truncated = true
	}

	// Output filtered tickets
	for _, summary := range filtered {
		line := formatTicketLine(summary)
		fprintln(out, line)
	}

	// Print summary if truncated
	if truncated {
		_, _ = fmt.Fprintf(out, "... and %d more (%d total)\n", remaining, total)
	}

	return hasErrors
}

func isValidStatus(status string) bool {
	return status == StatusOpen || status == StatusInProgress || status == StatusClosed
}

func formatTicketLine(summary *TicketSummary) string {
	var builder strings.Builder

	builder.WriteString(summary.ID)
	builder.WriteString(" [")
	builder.WriteString(summary.Status)
	builder.WriteString("] - ")
	builder.WriteString(summary.Title)

	if len(summary.BlockedBy) > 0 {
		builder.WriteString(" <- blocked-by: [")
		builder.WriteString(strings.Join(summary.BlockedBy, ", "))
		builder.WriteString("]")
	}

	return builder.String()
}
