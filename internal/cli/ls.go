package cli

import (
	"errors"
	"fmt"
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

func cmdLs(io *IO, cfg ticket.Config, workDir string, args []string) error {
	// Handle --help/-h
	if hasHelpFlag(args) {
		printLsHelp(io)

		return nil
	}

	opts, err := parseLsFlags(args)
	if err != nil {
		return err
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

	results, err := ticket.ListTickets(ticketDir, listOpts, nil)
	if err != nil {
		return err
	}

	// Separate valid tickets from errors
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

	// Output valid tickets
	for _, summary := range valid {
		io.Println(formatTicketLine(summary))
	}

	return nil
}

func parseLsFlags(args []string) (lsOptions, error) {
	flagSet := flag.NewFlagSet("ls", flag.ContinueOnError)
	flagSet.SetOutput(&strings.Builder{}) // discard output

	status := flagSet.String("status", "", "Filter by status")
	priority := flagSet.Int("priority", 0, "Filter by priority (1-4)")
	ticketType := flagSet.String("type", "", "Filter by type")
	limit := flagSet.Int("limit", defaultLimit, "Maximum tickets to show")
	offset := flagSet.Int("offset", 0, "Skip first N tickets")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		return lsOptions{}, parseErr
	}

	// Validate status if provided
	if flagSet.Changed("status") {
		validateErr := validateStatusFlag(*status)
		if validateErr != nil {
			return lsOptions{}, validateErr
		}
	}

	// Validate priority if provided
	if flagSet.Changed("priority") {
		if *priority < 1 || *priority > 4 {
			return lsOptions{}, errors.New("--priority must be 1-4")
		}
	}

	// Validate type if provided
	if flagSet.Changed("type") {
		if !ticket.IsValidTicketType(*ticketType) {
			return lsOptions{}, fmt.Errorf("invalid type: %s", *ticketType)
		}
	}

	// Validate limit
	if *limit < 0 {
		return lsOptions{}, errors.New("--limit must be non-negative")
	}

	// Validate offset
	if *offset < 0 {
		return lsOptions{}, errors.New("--offset must be non-negative")
	}

	return lsOptions{
		status:     *status,
		priority:   *priority,
		ticketType: *ticketType,
		limit:      *limit,
		offset:     *offset,
	}, nil
}

func printLsHelp(io *IO) {
	io.Println("Usage: tk ls [options]")
	io.Println("")
	io.Println("List all tickets. Output sorted by ID (oldest first).")
	io.Println("")
	io.Println("Options:")
	io.Println("  --status=<status>    Filter by status (open|in_progress|closed)")
	io.Println("  --priority=N         Filter by priority (1-4)")
	io.Println("  --type=<type>        Filter by type (bug|feature|task|epic|chore)")
	io.Println("  --limit=N            Max tickets to show [default: 100]")
	io.Println("  --offset=N           Skip first N tickets [default: 0]")
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

	if len(summary.BlockedBy) > 0 {
		builder.WriteString(" <- blocked-by: [")
		builder.WriteString(strings.Join(summary.BlockedBy, ", "))
		builder.WriteString("]")
	}

	return builder.String()
}
