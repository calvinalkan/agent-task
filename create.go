package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
)

var (
	errTitleRequired   = errors.New("title is required")
	errEmptyValue      = errors.New("empty value not allowed")
	errInvalidType     = errors.New("invalid type")
	errInvalidPriority = errors.New("invalid priority (must be 1-4)")
	errInvalidBlocker  = errors.New("blocker not found")
)

const createHelp = `  create <title>         Create ticket, prints ID
    -d, --description      Description text
    --design               Design notes
    --acceptance           Acceptance criteria
    -t, --type             Type (bug|feature|task|epic|chore) [default: task]
    -p, --priority         Priority 1-4, 1=most urgent [default: 2]
    -a, --assignee         Assignee [default: git user.name]
    --blocked-by           Blocker ticket ID (repeatable)`

//nolint:funlen,cyclop // command handler with validation
func cmdCreate(out io.Writer, errOut io.Writer, cfg Config, workDir string, args []string) int {
	// Handle --help/-h
	if hasHelpFlag(args) {
		fprintln(out, "Usage: tk create <title> [options]")
		fprintln(out, "")
		fprintln(out, "Create a new ticket. Prints ticket ID on success.")
		fprintln(out, "")
		fprintln(out, "Options:")
		fprintln(out, "  -d, --description    Description text")
		fprintln(out, "  --design             Design notes")
		fprintln(out, "  --acceptance         Acceptance criteria")
		fprintln(out, "  -t, --type           Type (bug|feature|task|epic|chore) [default: task]")
		fprintln(out, "  -p, --priority       Priority 1-4, 1=most urgent [default: 2]")
		fprintln(out, "  -a, --assignee       Assignee [default: git user.name]")
		fprintln(out, "  --blocked-by         Blocker ticket ID (repeatable)")

		return 0
	}

	flagSet := flag.NewFlagSet("create", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard) // We handle errors ourselves

	description := flagSet.StringP("description", "d", "", "Description text")
	design := flagSet.String("design", "", "Design notes")
	acceptance := flagSet.String("acceptance", "", "Acceptance criteria")
	ticketType := flagSet.StringP("type", "t", "task", "Type: bug|feature|task|epic|chore")
	priority := flagSet.IntP("priority", "p", DefaultPriority, "Priority 1-4 (1=most urgent)")
	assignee := flagSet.StringP("assignee", "a", "", "Assignee name")
	blockedBy := flagSet.StringArray("blocked-by", nil, "Blocker ticket ID (repeatable)")

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	// Title is first positional argument
	title := ""
	if flagSet.NArg() > 0 {
		title = flagSet.Arg(0)
	}

	if title == "" {
		fprintln(errOut, "error:", errTitleRequired)

		return 1
	}

	// Validate no empty values for flags that were explicitly set
	emptyErr := validateNotEmpty(flagSet, "description", *description)
	if emptyErr != nil {
		fprintln(errOut, "error:", emptyErr)

		return 1
	}

	emptyErr = validateNotEmpty(flagSet, "design", *design)
	if emptyErr != nil {
		fprintln(errOut, "error:", emptyErr)

		return 1
	}

	emptyErr = validateNotEmpty(flagSet, "acceptance", *acceptance)
	if emptyErr != nil {
		fprintln(errOut, "error:", emptyErr)

		return 1
	}

	emptyErr = validateNotEmpty(flagSet, "type", *ticketType)
	if emptyErr != nil {
		fprintln(errOut, "error:", emptyErr)

		return 1
	}

	emptyErr = validateNotEmpty(flagSet, "assignee", *assignee)
	if emptyErr != nil {
		fprintln(errOut, "error:", emptyErr)

		return 1
	}

	// Validate type
	if !IsValidType(*ticketType) {
		fprintln(errOut, "error:", errInvalidType, *ticketType)

		return 1
	}

	// Validate priority
	if !IsValidPriority(*priority) {
		fprintln(errOut, "error:", errInvalidPriority)

		return 1
	}

	// Resolve ticket directory
	ticketDir := cfg.TicketDir
	if !filepath.IsAbs(ticketDir) {
		ticketDir = filepath.Join(workDir, ticketDir)
	}

	// Validate blockers exist
	for _, blocker := range *blockedBy {
		if blocker == "" {
			fprintln(errOut, "error:", errEmptyValue, "--blocked-by")

			return 1
		}

		if !TicketExists(ticketDir, blocker) {
			fprintln(errOut, "error:", errInvalidBlocker, blocker)

			return 1
		}
	}

	// Get assignee default from git if not specified
	actualAssignee := *assignee
	if !flagSet.Changed("assignee") {
		actualAssignee = getGitUserName()
	}

	// Generate unique ID
	ticketID, err := GenerateUniqueID(ticketDir)
	if err != nil {
		fprintln(errOut, "error:", err)

		return 1
	}

	// Create ticket
	ticket := Ticket{
		ID:          ticketID,
		Status:      "open",
		BlockedBy:   *blockedBy,
		Created:     time.Now(),
		Type:        *ticketType,
		Priority:    *priority,
		Assignee:    actualAssignee,
		Title:       title,
		Description: *description,
		Design:      *design,
		Acceptance:  *acceptance,
	}

	_, writeErr := WriteTicket(ticketDir, ticket)
	if writeErr != nil {
		fprintln(errOut, "error:", writeErr)

		return 1
	}

	fprintln(out, ticketID)

	return 0
}

func validateNotEmpty(flagSet *flag.FlagSet, name, value string) error {
	if flagSet.Changed(name) && value == "" {
		return fmt.Errorf("%w: --%s", errEmptyValue, name)
	}

	return nil
}

func getGitUserName() string {
	//nolint:noctx // no context needed for simple git config lookup
	cmd := exec.Command("git", "config", "user.name")

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}
