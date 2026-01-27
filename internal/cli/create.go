package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/calvinalkan/agent-task/internal/ticket"

	flag "github.com/spf13/pflag"
)

var (
	errTitleRequired   = errors.New("title is required")
	errEmptyValue      = errors.New("empty value not allowed")
	errInvalidType     = errors.New("invalid type")
	errInvalidPriority = errors.New("invalid priority (must be 1-4)")
	errInvalidBlocker  = errors.New("blocker not found")
	errInvalidParent   = errors.New("parent not found")
	errParentClosed    = errors.New("parent ticket is closed")
)

// CreateCmd returns the create command.
func CreateCmd(cfg *ticket.Config) *Command {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.StringP("description", "d", "", "Description text")
	fs.String("design", "", "Design notes")
	fs.String("acceptance", "", "Acceptance criteria")
	fs.StringP("type", "t", "task", "Type: bug|feature|task|epic|chore")
	fs.IntP("priority", "p", ticket.DefaultPriority, "Priority 1-4 (1=most urgent)")
	fs.StringP("assignee", "a", "", "Assignee name")
	fs.StringArray("blocked-by", nil, "Blocker ticket ID (repeatable)")
	fs.String("parent", "", "Parent ticket ID")

	return &Command{
		Flags: fs,
		Usage: "create <title>",
		Short: "Create ticket, prints ID",
		Long: `Create a new ticket. Prints ticket ID on success.

If --parent is specified, the parent ticket must exist and not be closed.`,
		Exec: func(_ context.Context, io *IO, args []string) error {
			return execCreate(io, cfg, fs, args)
		},
	}
}

func execCreate(io *IO, cfg *ticket.Config, fs *flag.FlagSet, args []string) error {
	title := ""
	if len(args) > 0 {
		title = args[0]
	}

	if title == "" {
		return errTitleRequired
	}

	// Validate no empty values for flags that were explicitly set
	for _, name := range []string{"description", "design", "acceptance", "type", "assignee", "parent"} {
		v, _ := fs.GetString(name)
		if fs.Changed(name) && v == "" {
			return fmt.Errorf("%w: --%s", errEmptyValue, name)
		}
	}

	ticketType, _ := fs.GetString("type")
	if !ticket.IsValidType(ticketType) {
		return fmt.Errorf("%w: %s", errInvalidType, ticketType)
	}

	priority, _ := fs.GetInt("priority")
	if !ticket.IsValidPriority(priority) {
		return errInvalidPriority
	}

	parent, _ := fs.GetString("parent")
	if parent != "" {
		if !ticket.Exists(cfg.TicketDirAbs, parent) {
			return fmt.Errorf("%w: %s", errInvalidParent, parent)
		}

		// Check parent is not closed
		parentPath := ticket.Path(cfg.TicketDirAbs, parent)

		parentStatus, statusErr := ticket.ReadTicketStatus(parentPath)
		if statusErr != nil {
			return fmt.Errorf("reading parent status: %w", statusErr)
		}

		if parentStatus == ticket.StatusClosed {
			return fmt.Errorf("%w: %s", errParentClosed, parent)
		}
	}

	blockedBy, _ := fs.GetStringArray("blocked-by")
	seenBlockers := make(map[string]bool)

	for _, blocker := range blockedBy {
		if blocker == "" {
			return fmt.Errorf("%w: --blocked-by", errEmptyValue)
		}

		if !ticket.Exists(cfg.TicketDirAbs, blocker) {
			return fmt.Errorf("%w: %s", errInvalidBlocker, blocker)
		}

		if seenBlockers[blocker] {
			return fmt.Errorf("duplicate blocker: %s", blocker)
		}

		seenBlockers[blocker] = true
	}

	description, _ := fs.GetString("description")
	design, _ := fs.GetString("design")
	acceptance, _ := fs.GetString("acceptance")
	assignee, _ := fs.GetString("assignee")

	tkt := ticket.Ticket{
		SchemaVersion: 1,
		Status:        "open",
		BlockedBy:     blockedBy,
		Parent:        parent,
		Created:       time.Now(),
		Type:          ticketType,
		Priority:      priority,
		Assignee:      assignee,
		Title:         title,
		Description:   description,
		Design:        design,
		Acceptance:    acceptance,
	}

	ticketID, ticketPath, writeErr := ticket.WriteTicketAtomic(cfg.TicketDirAbs, &tkt)
	if writeErr != nil {
		return fmt.Errorf("write ticket: %w", writeErr)
	}

	summary, parseErr := ticket.ParseTicketFrontmatter(ticketPath)
	if parseErr != nil {
		return fmt.Errorf("parse frontmatter: %w", parseErr)
	}

	cacheErr := ticket.UpdateCacheAfterTicketWrite(cfg.TicketDirAbs, ticketID+".md", &summary)
	if cacheErr != nil {
		return fmt.Errorf("update cache: %w", cacheErr)
	}

	io.Println(ticketID)

	return nil
}
