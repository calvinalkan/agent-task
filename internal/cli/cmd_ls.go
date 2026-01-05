package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"tk/internal/ticket"

	flag "github.com/spf13/pflag"
)

const defaultLimit = 100

// lsOptions holds parsed ls command options.
type lsOptions struct {
	status     string
	priority   int
	ticketType string
	limit      int
	offset     int
}

func cmdLs(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
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

	// List tickets with options - filtering happens in ListTickets
	listOpts := ticket.ListTicketsOptions{
		Status:   opts.status,
		Priority: opts.priority,
		Type:     opts.ticketType,
		Limit:    opts.limit,
		Offset:   opts.offset,
	}

	results, err := ticket.ListTickets(ticketDir, listOpts, errOut)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Output tickets and track errors
	hasErrors := outputTickets(out, errOut, results)

	if hasErrors {
		return 1
	}

	return 0
}

func parseLsFlags(errOut io.Writer, args []string) (lsOptions, int) {
	flagSet := flag.NewFlagSet("ls", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	status := flagSet.String("status", "", "Filter by status")
	priority := flagSet.Int("priority", 0, "Filter by priority (1-4)")
	ticketType := flagSet.String("type", "", "Filter by type")
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

	// Validate priority if provided
	if flagSet.Changed("priority") {
		if *priority < 1 || *priority > 4 {
			fprintln(errOut, "error: --priority must be 1-4")

			return lsOptions{}, 1
		}
	}

	// Validate type if provided
	if flagSet.Changed("type") {
		if !ticket.IsValidTicketType(*ticketType) {
			fprintln(errOut, "error: invalid type:", *ticketType)

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
		status:     *status,
		priority:   *priority,
		ticketType: *ticketType,
		limit:      *limit,
		offset:     *offset,
	}, 0
}

func printLsHelp(out io.Writer) {
	fprintln(out, "Usage: tk ls [options]")
	fprintln(out, "")
	fprintln(out, "List all tickets. Output sorted by ID (oldest first).")
	fprintln(out, "")
	fprintln(out, "Options:")
	fprintln(out, "  --status=<status>    Filter by status (open|in_progress|closed)")
	fprintln(out, "  --priority=N         Filter by priority (1-4)")
	fprintln(out, "  --type=<type>        Filter by type (bug|feature|task|epic|chore)")
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
	results []ticket.Result,
) bool {
	hasErrors := false

	for _, result := range results {
		if result.Err != nil {
			fprintln(errOut, "warning:", result.Path+":", result.Err)

			hasErrors = true

			continue
		}

		line := formatTicketLine(result.Summary)
		fprintln(out, line)
	}

	return hasErrors
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

	if len(summary.BlockedBy) > 0 {
		builder.WriteString(" <- blocked-by: [")
		builder.WriteString(strings.Join(summary.BlockedBy, ", "))
		builder.WriteString("]")
	}

	return builder.String()
}
