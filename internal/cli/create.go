package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tk/internal/ticket"

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

func cmdCreate(out io.Writer, errOut io.Writer, cfg ticket.Config, workDir string, args []string) int {
	flagSet := flag.NewFlagSet("create", flag.ContinueOnError)
	flagSet.SetOutput(errOut)
	flagSet.Usage = func() {
		w := flagSet.Output()
		fprintf(w, "Usage: tk create <title> [options]\n\n")
		fprintf(w, "Create a new ticket. Prints ticket ID on success.\n\n")
		fprintf(w, "Options:\n")
		flagSet.PrintDefaults()
	}

	description := flagSet.StringP("description", "d", "", "Description text")
	design := flagSet.String("design", "", "Design notes")
	acceptance := flagSet.String("acceptance", "", "Acceptance criteria")
	ticketType := flagSet.StringP("type", "t", "task", "Type: bug|feature|task|epic|chore")
	priority := flagSet.IntP("priority", "p", ticket.DefaultPriority, "Priority 1-4 (1=most urgent)")
	assignee := flagSet.StringP("assignee", "a", "", "Assignee name")
	blockedBy := flagSet.StringArray("blocked-by", nil, "Blocker ticket ID (repeatable)")

	if hasHelpFlag(args) {
		flagSet.SetOutput(out)
		flagSet.Usage()

		return 0
	}

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		fprintf(errOut, "error: %v\n\n", parseErr)
		flagSet.Usage()

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
	for _, check := range []struct{ name, value string }{
		{"description", *description},
		{"design", *design},
		{"acceptance", *acceptance},
		{"type", *ticketType},
		{"assignee", *assignee},
	} {
		err := validateNotEmpty(flagSet, check.name, check.value)
		if err != nil {
			fprintln(errOut, "error:", err)

			return 1
		}
	}

	// Validate type
	if !ticket.IsValidType(*ticketType) {
		fprintln(errOut, "error:", errInvalidType, *ticketType)

		return 1
	}

	// Validate priority
	if !ticket.IsValidPriority(*priority) {
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

		if !ticket.Exists(ticketDir, blocker) {
			fprintln(errOut, "error:", errInvalidBlocker, blocker)

			return 1
		}
	}

	// Get assignee default from git if not specified
	actualAssignee := *assignee
	if !flagSet.Changed("assignee") {
		actualAssignee = getGitUserName()
	}

	// Create ticket (ID will be generated atomically)
	tkt := ticket.Ticket{
		SchemaVersion: 1,
		Status:        "open",
		BlockedBy:     *blockedBy,
		Created:       time.Now(),
		Type:          *ticketType,
		Priority:      *priority,
		Assignee:      actualAssignee,
		Title:         title,
		Description:   *description,
		Design:        *design,
		Acceptance:    *acceptance,
	}

	ticketID, ticketPath, writeErr := ticket.WriteTicketAtomic(ticketDir, &tkt)
	if writeErr != nil {
		fprintln(errOut, "error:", writeErr)

		return 1
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(ticketPath)
	if parseErr != nil {
		fprintln(errOut, "error:", parseErr)

		return 1
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(ticketDir, ticketID+".md", &summary)
	if cacheErr != nil {
		fprintln(errOut, "error:", cacheErr)

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
	cmd := exec.Command("git", "config", "user.name")

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}
