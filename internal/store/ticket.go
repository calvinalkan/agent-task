package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/calvinalkan/agent-task/internal/frontmatter"
)

// TicketFrontmatter contains the logical ticket data stored in frontmatter.
// Returned by Query operations. Optional fields use pointer types: nil means not set.
type TicketFrontmatter struct {
	// Ticket identifier (UUIDv7)
	ID uuid.UUID `json:"id"`

	// Status (required, non-empty)
	Status string `json:"status"`

	// Type (required, non-empty)
	Type string `json:"type"`

	// Priority (required, >= 1)
	Priority int64 `json:"priority"`

	// Title from frontmatter
	Title string `json:"title"`

	// Creation timestamp (UTC)
	CreatedAt time.Time `json:"created_at"`

	// Assigned user/agent (optional)
	Assignee *string `json:"assignee,omitempty"`

	// Parent ticket ID for subtasks (optional)
	Parent *uuid.UUID `json:"parent,omitempty"`

	// External reference like GitHub issue URL (optional)
	ExternalRef *string `json:"external_ref,omitempty"`

	// Closure timestamp (optional, must not be zero if set)
	ClosedAt *time.Time `json:"closed_at,omitempty"`

	// Blocker ticket IDs (optional)
	BlockedBy uuid.UUIDs `json:"blocked_by,omitempty"`
}

// Ticket extends TicketMeta with derived fields, storage metadata, and body.
// Returned by Get; required by Put.
type Ticket struct {
	TicketFrontmatter

	// 12-char Crockford base32, derived from ID
	ShortID string `json:"short_id"`

	// Canonical relative path, derived from ID
	Path string `json:"path"`

	// MtimeNS is the file modification time in nanoseconds, used for cache
	// invalidation during reindex. This value may be stale - it reflects the
	// mtime at the time of indexing, not necessarily the current file state.
	MtimeNS int64 `json:"mtime_ns"`

	// Freeform content after frontmatter
	Body string `json:"body"`
}

// StringPtr returns a pointer to the given string. Helper for optional fields.
func StringPtr(s string) *string {
	return &s
}

// TimePtr returns a pointer to the given time. Helper for optional fields.
func TimePtr(t time.Time) *time.Time {
	return &t
}

// NewTicket creates a new ticket with required fields and a generated UUIDv7.
// CreatedAt is set to the current UTC time. Call Validate() before Put().
func NewTicket(title, ticketType, status string, priority int64) (*Ticket, error) {
	id, err := newUUIDv7()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}

	relPath, err := pathFromID(id)
	if err != nil {
		return nil, fmt.Errorf("derive path: %w", err)
	}

	shortID, err := shortIDFromUUID(id)
	if err != nil {
		return nil, fmt.Errorf("derive short id: %w", err)
	}

	return &Ticket{
		TicketFrontmatter: TicketFrontmatter{
			ID:        id,
			Status:    status,
			Type:      ticketType,
			Priority:  priority,
			Title:     title,
			CreatedAt: time.Now().UTC(),
		},
		ShortID: shortID,
		Path:    relPath,
	}, nil
}

// validate checks all required fields and returns an error if any are invalid.
func (t *Ticket) validate() error {
	if t == nil {
		return errors.New("ticket is nil")
	}

	if t.ID == uuid.Nil {
		return errors.New("id is required")
	}

	if t.ID.Version() != 7 {
		return fmt.Errorf("id %q is not UUIDv7", t.ID)
	}

	expectedPath, err := pathFromID(t.ID)
	if err != nil {
		return fmt.Errorf("derive path for %q: %w", t.ID, err)
	}

	if t.Path != expectedPath {
		return fmt.Errorf("path %q does not match id %q (expected %q)", t.Path, t.ID, expectedPath)
	}

	if t.ShortID == "" {
		return errors.New("short_id is required")
	}

	expectedShort, err := shortIDFromUUID(t.ID)
	if err != nil {
		return fmt.Errorf("derive short_id for %q: %w", t.ID, err)
	}

	if t.ShortID != expectedShort {
		return fmt.Errorf("short_id %q does not match id %q (expected %q)", t.ShortID, t.ID, expectedShort)
	}

	if t.Status == "" {
		return errors.New("status is required")
	}

	if t.Type == "" {
		return errors.New("type is required")
	}

	if t.Priority < 1 {
		return fmt.Errorf("priority %d is invalid (must be >= 1)", t.Priority)
	}

	if t.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}

	if t.Title == "" {
		return errors.New("title is required")
	}

	if t.ClosedAt != nil && t.ClosedAt.IsZero() {
		return errors.New("closed_at must not be zero")
	}

	return nil
}

// frontmatterKeyOrder returns the canonical key order for ticket frontmatter.
// Required keys first (id, schema_version), then alphabetical.
func frontmatterKeyOrder(fm frontmatter.Frontmatter) []string {
	keys := make([]string, 0, len(fm))
	for key := range fm {
		if key == "id" || key == "schema_version" {
			continue
		}

		keys = append(keys, key)
	}

	// Sort remaining keys alphabetically
	for i := range len(keys) - 1 {
		for j := i + 1; j < len(keys); j++ {
			if keys[i] > keys[j] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}

	ordered := make([]string, 0, len(fm))
	ordered = append(ordered, "id", "schema_version")
	ordered = append(ordered, keys...)

	return ordered
}

// marshalFile renders the ticket to file bytes (frontmatter + body).
func (t *Ticket) marshalFile() ([]byte, error) {
	fm := frontmatter.Frontmatter{
		"id":             frontmatter.StringValue(t.ID.String()),
		"schema_version": frontmatter.IntValue(1),
		"created":        frontmatter.StringValue(t.CreatedAt.UTC().Format(time.RFC3339)),
		"priority":       frontmatter.IntValue(t.Priority),
		"status":         frontmatter.StringValue(t.Status),
		"title":          frontmatter.StringValue(t.Title),
		"type":           frontmatter.StringValue(t.Type),
	}

	if t.Assignee != nil {
		fm["assignee"] = frontmatter.StringValue(*t.Assignee)
	}

	if len(t.BlockedBy) > 0 {
		blockedByStrs := make([]string, len(t.BlockedBy))
		for i, b := range t.BlockedBy {
			blockedByStrs[i] = b.String()
		}

		fm["blocked-by"] = frontmatter.ListValue(blockedByStrs)
	}

	if t.ClosedAt != nil {
		fm["closed"] = frontmatter.StringValue(t.ClosedAt.UTC().Format(time.RFC3339))
	}

	if t.ExternalRef != nil {
		fm["external-ref"] = frontmatter.StringValue(*t.ExternalRef)
	}

	if t.Parent != nil {
		fm["parent"] = frontmatter.StringValue(t.Parent.String())
	}

	yamlStr, err := fm.MarshalYAML(frontmatter.WithKeyOrder(frontmatterKeyOrder(fm)))
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}

	var b strings.Builder
	b.WriteString(yamlStr)

	if t.Body != "" {
		b.WriteString("\n")
		b.WriteString(t.Body)

		if !strings.HasSuffix(t.Body, "\n") {
			b.WriteString("\n")
		}
	}

	return []byte(b.String()), nil
}

// parseTicketFile parses a ticket file (frontmatter + body) into a Ticket.
// It extracts values from frontmatter, builds the ticket, and validates it.
func parseTicketFile(data []byte, relPath string, mtimeNS int64) (*Ticket, error) {
	fm, tail, err := frontmatter.ParseFrontmatter(data, frontmatter.WithLineLimit(rebuildFrontmatterLineLimit))
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}

	// Check schema version first
	schema, ok := fm.GetInt("schema_version")
	if !ok {
		return nil, errors.New("missing or invalid schema_version")
	}

	if schema != int64(currentSchemaVersion) {
		return nil, fmt.Errorf("schema_version %d is unsupported (expected %d)", schema, currentSchemaVersion)
	}

	// Extract ID and derive computed fields
	idRaw, ok := fm.GetString("id")
	if !ok {
		return nil, errors.New("missing id")
	}

	id, err := parseUUIDv7(idRaw)
	if err != nil {
		return nil, err
	}

	shortID, err := shortIDFromUUID(id)
	if err != nil {
		return nil, err
	}

	path, err := pathFromID(id)
	if err != nil {
		return nil, err
	}

	// Check file is at the expected path
	if relPath != path {
		return nil, fmt.Errorf("file at %q but id %q expects %q", relPath, idRaw, path)
	}

	// Extract required fields
	status, ok := fm.GetString("status")
	if !ok {
		return nil, errors.New("missing status")
	}

	kind, ok := fm.GetString("type")
	if !ok {
		return nil, errors.New("missing type")
	}

	priority, ok := fm.GetInt("priority")
	if !ok {
		return nil, errors.New("missing priority")
	}

	title, ok := fm.GetString("title")
	if !ok {
		return nil, errors.New("missing title")
	}

	createdRaw, ok := fm.GetString("created")
	if !ok {
		return nil, errors.New("missing created")
	}

	createdAt, err := parseRFC3339(createdRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid created: %w", err)
	}

	// Extract optional fields
	var assignee, externalRef *string
	if s, ok := fm.GetString("assignee"); ok {
		assignee = &s
	}

	var parent *uuid.UUID

	if s, ok := fm.GetString("parent"); ok {
		p, pErr := parseUUIDv7(s)
		if pErr != nil {
			return nil, fmt.Errorf("invalid parent: %w", pErr)
		}

		parent = &p
	}

	if s, ok := fm.GetString("external-ref"); ok {
		externalRef = &s
	}

	var closedAt *time.Time

	if s, ok := fm.GetString("closed"); ok {
		parsed, pErr := parseRFC3339(s)
		if pErr == nil {
			closedAt = &parsed
		}
	}

	blockedByStrs, _ := fm.GetList("blocked-by")

	var blockedBy uuid.UUIDs

	seen := make(map[uuid.UUID]bool)

	for _, b := range blockedByStrs {
		bid, bErr := parseUUIDv7(b)
		if bErr != nil {
			return nil, fmt.Errorf("invalid blocked-by: %w", bErr)
		}

		if seen[bid] {
			return nil, fmt.Errorf("blocked-by contains duplicate %q", b)
		}

		seen[bid] = true
		blockedBy = append(blockedBy, bid)
	}

	ticket := &Ticket{
		TicketFrontmatter: TicketFrontmatter{
			ID:          id,
			Status:      status,
			Type:        kind,
			Priority:    priority,
			Title:       title,
			CreatedAt:   createdAt.UTC(),
			Assignee:    assignee,
			Parent:      parent,
			ExternalRef: externalRef,
			ClosedAt:    closedAt,
			BlockedBy:   blockedBy,
		},
		ShortID: shortID,
		Path:    path,
		MtimeNS: mtimeNS,
		Body:    strings.TrimRight(string(tail), "\n"),
	}

	err = ticket.validate()
	if err != nil {
		return nil, fmt.Errorf("invalid ticket: %w", err)
	}

	return ticket, nil
}

func parseRFC3339(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time: %w", err)
	}

	return t.UTC(), nil
}
