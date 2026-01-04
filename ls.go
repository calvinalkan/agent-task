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

// lsOptions holds parsed ls command options.
type lsOptions struct {
	status string
	limit  int
	offset int
}

func cmdLs(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printLsHelp(out)

		return 0
	}

	opts, parseCode := parseLsFlags(errOut, args)
	if parseCode != 0 {
		return parseCode
	}

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// List tickets with options
	listOpts := ListTicketsOptions{
		NeedAll: opts.status != "", // need all if filtering by status
		Limit:   opts.limit,
		Offset:  opts.offset,
	}

	results, err := ListTickets(ticketDir, listOpts)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Output tickets and track errors
	hasErrors := outputTickets(out, errOut, results, opts.status, opts.limit, opts.offset)

	if hasErrors {
		return 1
	}

	return 0
}

func parseLsFlags(errOut io.Writer, args []string) (lsOptions, int) {
	flagSet := flag.NewFlagSet("ls", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	status := flagSet.String("status", "", "Filter by status")
	limit := flagSet.Int("limit", defaultLimit, "Maximum tickets to show")
	offset := flagSet.Int("offset", 0, "Skip first N tickets")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return lsOptions{}, 1
	}

	// Validate status if provided
	if flagSet.Changed("status") {
		validateErr := validateStatusFlag(*status)
		if validateErr != nil {
			fprintln(errOut, "error:", validateErr)

			return lsOptions{}, 1
		}
	}

	// Validate limit
	if *limit < 0 {
		fprintln(errOut, "error: --limit must be non-negative")

		return lsOptions{}, 1
	}

	// Validate offset
	if *offset < 0 {
		fprintln(errOut, "error: --offset must be non-negative")

		return lsOptions{}, 1
	}

	return lsOptions{
		status: *status,
		limit:  *limit,
		offset: *offset,
	}, 0
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

	// Filter by status and collect errors
	// Note: offset/limit already applied by ListTickets when no status filter
	// When status filter is used, we need to apply offset/limit here after filtering
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

	// Apply offset/limit only when status filter was used
	// (otherwise ListTickets already applied them)
	if statusFilter != "" {
		// Check for out-of-bounds offset
		if offset > 0 && offset >= len(filtered) {
			fprintln(errOut, "error: offset out of bounds")

			return true
		}

		if offset > 0 {
			filtered = filtered[offset:]
		}

		if limit > 0 && limit < len(filtered) {
			filtered = filtered[:limit]
		}
	}

	// Output filtered tickets
	for _, summary := range filtered {
		line := formatTicketLine(summary)
		fprintln(out, line)
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
