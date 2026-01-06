package cli

import (
	"bytes"
	"errors"
	"fmt"
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
    -a, --assignee         Assignee
    --blocked-by           Blocker ticket ID (repeatable)`

func cmdCreate(o *IO, cfg ticket.Config, args []string) error {
	var helpBuf bytes.Buffer

	flagSet := flag.NewFlagSet("create", flag.ContinueOnError)
	flagSet.SetOutput(&helpBuf)
	flagSet.Usage = func() {
		w := flagSet.Output()
		fmt.Fprintf(w, "Usage: tk create <title> [options]\n\n")
		fmt.Fprintf(w, "Create a new ticket. Prints ticket ID on success.\n\n")
		fmt.Fprintf(w, "Options:\n")
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
		flagSet.Usage()
		o.Printf("%s", helpBuf.String())

		return nil
	}

	parseErr := flagSet.Parse(args)
	if parseErr != nil {
		return fmt.Errorf("%w\n\n%s", parseErr, helpBuf.String())
	}

	// Title is first positional argument
	title := ""
	if flagSet.NArg() > 0 {
		title = flagSet.Arg(0)
	}

	if title == "" {
		return errTitleRequired
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
			return err
		}
	}

	// Validate type
	if !ticket.IsValidType(*ticketType) {
		return fmt.Errorf("%w: %s", errInvalidType, *ticketType)
	}

	// Validate priority
	if !ticket.IsValidPriority(*priority) {
		return errInvalidPriority
	}

	// Validate blockers exist
	for _, blocker := range *blockedBy {
		if blocker == "" {
			return fmt.Errorf("%w: --blocked-by", errEmptyValue)
		}

		if !ticket.Exists(cfg.TicketDirAbs, blocker) {
			return fmt.Errorf("%w: %s", errInvalidBlocker, blocker)
		}
	}

	// Create ticket (ID will be generated atomically)
	tkt := ticket.Ticket{
		SchemaVersion: 1,
		Status:        "open",
		BlockedBy:     *blockedBy,
		Created:       time.Now(),
		Type:          *ticketType,
		Priority:      *priority,
		Assignee:      *assignee,
		Title:         title,
		Description:   *description,
		Design:        *design,
		Acceptance:    *acceptance,
	}

	ticketID, ticketPath, writeErr := ticket.WriteTicketAtomic(cfg.TicketDirAbs, &tkt)
	if writeErr != nil {
		return writeErr
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(ticketPath)
	if parseErr != nil {
		return parseErr
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(cfg.TicketDirAbs, ticketID+".md", &summary)
	if cacheErr != nil {
		return cacheErr
	}

	o.Println(ticketID)

	return nil
}

func validateNotEmpty(flagSet *flag.FlagSet, name, value string) error {
	if flagSet.Changed(name) && value == "" {
		return fmt.Errorf("%w: --%s", errEmptyValue, name)
	}

	return nil
}
