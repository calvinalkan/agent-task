// Package spec defines an in-memory oracle for tk's observable ticket semantics.
//
// This is the source of truth for what correct behavior looks like. If the real
// implementation disagrees with this spec, the implementation is wrong. This
// package is used by fuzz tests to verify the implementation against the model.
//
// Think of this as running the entire tk program purely in memory. No matter what
// sequence of operations you perform, this model should always produce the correct
// result. The real implementation (with YAML files, caching, filesystem operations)
// must behave identically to this in-memory model.
//
// The spec models how the system *should* behave (validation + state transitions)
// without relying on implementation details like YAML parsing, cache formats, or
// filesystem layout.
//
// Design principles:
//
//   - Simple over performant. Readability and obviousness matter more than loop
//     efficiency, allocations, or any performance optimization. The code should
//     be obviously correct by inspection.
//
//   - Explicit over clever. No magic, no tricks. If something is happening, it
//     should be visible in the code.
//
//   - No dependencies beyond the standard library.
//
//   - Panics indicate bugs in the spec itself (invariant violations). Errors
//     indicate invalid user input that the real implementation should also reject.
//
//   - All input methods accept only primitive types (string, int, bool). This
//     simulates end-to-end testing where inputs come from CLI arguments or user
//     input. Typed values like Status, Priority, and Type are internal only.
//
// Input conventions:
//
//   - All methods accept struct inputs, never bare primitives. This ensures
//     consistency and allows adding fields without breaking signatures.
//
//   - UserXxxInput structs contain user-provided values (from CLI arguments).
//     Example: UserCreateInput has Title, Description, Type, Priority, etc.
//
//   - FuzzXxxInput structs contain values provided by the fuzz test runner,
//     not user-provided. Example: FuzzCreateInput has ID and CreatedAt.
//     This includes any generated values like IDs or timestamps that the real
//     CLI would produce. The spec does not generate these; it receives them
//     from the layer above (the fuzz test harness or real CLI).
//
//   - When a method needs both, the order is always (user, fuzz). Most methods
//     only need user input, so this keeps the common case simple.
//
// This package is designed to be simple enough to not need tests.
package spec

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrCode is a stable error code for programmatic error handling.
type ErrCode string

// Stable error codes for programmatic error handling.
const (
	ErrIDRequired           ErrCode = "id_required"
	ErrTicketNotFound       ErrCode = "ticket_not_found"
	ErrTicketAlreadyExists  ErrCode = "ticket_already_exists"
	ErrTitleRequired        ErrCode = "title_required"
	ErrInvalidType          ErrCode = "invalid_type"
	ErrInvalidPriority      ErrCode = "invalid_priority"
	ErrInvalidStatus        ErrCode = "invalid_status"
	ErrInvalidTimestamp     ErrCode = "invalid_timestamp"
	ErrBlockerIDRequired    ErrCode = "blocker_id_required"
	ErrBlockerNotFound      ErrCode = "blocker_not_found"
	ErrCantBlockSelf        ErrCode = "cant_block_self"
	ErrAlreadyBlocked       ErrCode = "already_blocked"
	ErrNotBlocked           ErrCode = "not_blocked"
	ErrBlockerCycle         ErrCode = "blocker_cycle"
	ErrParentNotFound       ErrCode = "parent_not_found"
	ErrParentAlreadyClosed  ErrCode = "parent_already_closed"
	ErrParentNotStarted     ErrCode = "parent_not_started"
	ErrHasOpenChildren      ErrCode = "has_open_children"
	ErrHasOpenBlockers      ErrCode = "has_open_blockers"
	ErrAncestorNotReady     ErrCode = "ancestor_not_ready"
	ErrCantStartNotOpen     ErrCode = "cant_start_not_open"
	ErrCantCloseOpen        ErrCode = "cant_close_open"
	ErrCantCloseClosed      ErrCode = "cant_close_already_closed"
	ErrCantReopenOpen       ErrCode = "cant_reopen_open"
	ErrCantReopenInProgress ErrCode = "cant_reopen_in_progress"
	ErrClosedBeforeCreated  ErrCode = "closed_before_created"
	ErrStartedBeforeCreated ErrCode = "started_before_created"
	ErrInvalidOffset        ErrCode = "invalid_offset"
	ErrInvalidLimit         ErrCode = "invalid_limit"
	ErrOffsetOutOfBounds    ErrCode = "offset_out_of_bounds"
	ErrDuplicateBlocker     ErrCode = "duplicate_blocker"
	ErrTicketClosed         ErrCode = "ticket_closed"
	ErrClaimedByRequired    ErrCode = "claimed_by_required"
	ErrContentRequired      ErrCode = "content_required"
	ErrEmptyTag             ErrCode = "empty_tag"
	ErrDuplicateTag         ErrCode = "duplicate_tag"
)

// KV is a key-value pair for error context.
type KV struct {
	K string
	V string
}

// Error is a structured error with a code and context.
// All spec methods return *Error (or nil on success).
type Error struct {
	Code    ErrCode
	Context []KV
}

// Error formats the error as logfmt: code=xxx key="value" key="value".
func (e *Error) Error() string {
	var builder strings.Builder
	builder.WriteString("code=")
	builder.WriteString(string(e.Code))

	for _, kv := range e.Context {
		builder.WriteString(" ")
		builder.WriteString(kv.K)
		builder.WriteString("=")
		fmt.Fprintf(&builder, "%q", kv.V)
	}

	return builder.String()
}

func newErr(code ErrCode, kvs ...KV) *Error {
	return &Error{Code: code, Context: kvs}
}

func kv(k, v string) KV {
	return KV{K: k, V: v}
}

// Status represents the lifecycle state of a ticket.
// Valid values are StatusOpen, StatusInProgress, and StatusClosed.
type Status string

// Valid status values.
const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusClosed     Status = "closed"
)

// Priority represents the urgency of a ticket.
// Lower values are higher priority: PriorityCritical (1) is most urgent,
// PriorityLow (4) is least urgent.
type Priority int

// Valid priority values; lower is more urgent.
const (
	PriorityCritical Priority = 1
	PriorityHigh     Priority = 2
	PriorityMedium   Priority = 3
	PriorityLow      Priority = 4
)

// Type categorizes the nature of a ticket.
// Valid values are TypeBug, TypeFeature, TypeTask, TypeEpic, and TypeChore.
type Type string

// Valid ticket type values.
const (
	TypeBug     Type = "bug"
	TypeFeature Type = "feature"
	TypeTask    Type = "task"
	TypeEpic    Type = "epic"
	TypeChore   Type = "chore"
)

// Default values for new tickets.
const (
	DefaultType     = TypeTask
	DefaultPriority = PriorityHigh
)

// Ticket represents the complete state of a single ticket.
// All fields are set by Create and may be modified by state transitions.
type Ticket struct {
	ID        string
	CreatedAt time.Time
	Title     string
	Content   string // freeform markdown body; must be non-empty
	Type      Type
	Priority  Priority
	Status    Status
	ParentID  string // empty if no parent
	BlockedBy []string
	Tags      []string  // free-form tags; immutable after creation
	ClaimedBy string    // who is working on it; empty if not claimed
	ClaimedAt time.Time // when it was claimed; zero if not claimed
	ClosedAt  time.Time // zero value if not closed
}

// Model tracks the expected state of all tickets.
// It maintains both a map for O(1) lookup and a slice for creation order.
type Model struct {
	tickets map[string]*Ticket
	order   []string // IDs in creation order
}

// New returns a new empty model.
func New() *Model {
	return &Model{tickets: make(map[string]*Ticket)}
}

// FuzzCreateInput contains values provided by the fuzz test runner, not user-provided.
//
// The fuzz runner is responsible for generating IDs and timestamps that satisfy:
//   - IDs are lexicographically sortable in creation order
//   - CreatedAt timestamps are chronologically ordered
//   - Sorting by ID and sorting by CreatedAt produce the same order
type FuzzCreateInput struct {
	ID        string // generated ticket ID
	CreatedAt string // ISO 8601 timestamp of creation
}

// UserCreateInput contains user-provided values for creating a ticket.
//
// Zero values are treated as "not specified" and will use defaults:
//   - Type == "" uses DefaultType (task)
//   - Priority == 0 uses DefaultPriority (high)
//
// All other zero values are valid (no parent, no blockers, no tags).
type UserCreateInput struct {
	Title     string
	Content   string // freeform markdown body; must be non-empty
	Type      string
	Priority  int
	ParentID  string // empty if no parent
	BlockedBy []string
	Tags      []string // free-form tags; no empty strings, no duplicates
}

// Create creates a new ticket and returns the ID.
//
// The ticket is created with Status=StatusOpen and ClosedAt as zero time.
// The ticket is appended to the creation order.
//
// Returns an error if:
//   - fuzz.ID is empty
//   - fuzz.CreatedAt is not a valid ISO 8601 timestamp
//   - a ticket with the given ID already exists
//   - Title is empty or whitespace-only
//   - Content is empty or whitespace-only
//   - Type is non-empty but not a valid Type
//   - Priority is non-zero but not in range 1-4
//   - ParentID is non-empty but references a non-existent or closed ticket
//   - any BlockedBy ID is empty, references a non-existent ticket, or is duplicated
//   - any Tag is empty or duplicated
//
// Panics if:
//   - fuzz.ID is lexicographically less than or equal to the previous ID (violates ordering invariant)
//   - fuzz.CreatedAt is chronologically before the previous CreatedAt (violates ordering invariant)
func (m *Model) Create(user *UserCreateInput, fuzz FuzzCreateInput) (string, *Error) {
	if fuzz.ID == "" {
		return "", newErr(ErrIDRequired)
	}

	createdAt, err := toISO8601(fuzz.CreatedAt)
	if err != nil {
		return "", err
	}

	if _, exists := m.tickets[fuzz.ID]; exists {
		return "", newErr(ErrTicketAlreadyExists, kv("id", fuzz.ID))
	}

	if strings.TrimSpace(user.Title) == "" {
		return "", newErr(ErrTitleRequired, kv("title", user.Title))
	}

	if strings.TrimSpace(user.Content) == "" {
		return "", newErr(ErrContentRequired)
	}

	if user.Type != "" {
		_, typeErr := toType(user.Type)
		if typeErr != nil {
			return "", typeErr
		}
	}

	if user.Priority != 0 {
		_, prioErr := toPriority(user.Priority)
		if prioErr != nil {
			return "", prioErr
		}
	}

	if user.ParentID != "" {
		parent, ok := m.tickets[user.ParentID]
		if !ok {
			return "", newErr(ErrParentNotFound, kv("parent_id", user.ParentID))
		}

		if parent.Status == StatusClosed {
			return "", newErr(ErrParentAlreadyClosed,
				kv("parent_id", user.ParentID),
				kv("status", string(parent.Status)))
		}
	}

	seen := make(map[string]bool)

	for _, blockerID := range user.BlockedBy {
		if blockerID == "" {
			return "", newErr(ErrBlockerIDRequired, kv("blocker_id", blockerID))
		}

		if _, ok := m.tickets[blockerID]; !ok {
			return "", newErr(ErrBlockerNotFound, kv("blocker_id", blockerID))
		}

		if seen[blockerID] {
			return "", newErr(ErrDuplicateBlocker, kv("blocker_id", blockerID))
		}

		seen[blockerID] = true
	}

	seenTags := make(map[string]bool)

	for _, tag := range user.Tags {
		if tag == "" {
			return "", newErr(ErrEmptyTag)
		}

		if seenTags[tag] {
			return "", newErr(ErrDuplicateTag, kv("tag", tag))
		}

		seenTags[tag] = true
	}

	// Enforce ordering invariants
	if len(m.order) > 0 {
		lastID := m.order[len(m.order)-1]
		lastTicket := m.tickets[lastID]

		if fuzz.ID <= lastID {
			panic(fmt.Sprintf("invalid ID ordering: %q must be lexicographically > %q", fuzz.ID, lastID))
		}

		if createdAt.Before(lastTicket.CreatedAt) {
			panic(fmt.Sprintf("invalid CreatedAt ordering: %q must be >= %q", fuzz.CreatedAt, lastTicket.CreatedAt.Format(time.RFC3339)))
		}
	}

	ticketType := Type(user.Type)
	if user.Type == "" {
		ticketType = DefaultType
	}

	priority := Priority(user.Priority)
	if user.Priority == 0 {
		priority = DefaultPriority
	}

	m.tickets[fuzz.ID] = &Ticket{
		ID:        fuzz.ID,
		CreatedAt: createdAt,
		Title:     user.Title,
		Content:   user.Content,
		Type:      ticketType,
		Priority:  priority,
		Status:    StatusOpen,
		ParentID:  user.ParentID,
		BlockedBy: slices.Clone(user.BlockedBy),
		Tags:      slices.Clone(user.Tags),
		ClaimedBy: "",
		ClaimedAt: time.Time{},
		ClosedAt:  time.Time{},
	}
	m.order = append(m.order, fuzz.ID)

	return fuzz.ID, nil
}

// UserStartInput contains user-provided values for starting a ticket.
type UserStartInput struct {
	ID        string // ticket ID to start
	ClaimedBy string // who is claiming the ticket
}

// FuzzStartInput contains values provided by the fuzz test runner for starting a ticket.
type FuzzStartInput struct {
	ClaimedAt string // ISO 8601 timestamp of when the ticket was claimed
}

// Start transitions a ticket from StatusOpen to StatusInProgress.
//
// Sets ClaimedBy and ClaimedAt to record who is working on it and when.
//
// A ticket can be started if:
//   - Status is Open
//   - all direct blockers are Closed
//   - parent is not Open (must be InProgress or Closed, or no parent)
//   - all ancestors are unblocked (no ancestor has open blockers)
//
// Returns an error if:
//   - user.ClaimedBy is empty
//   - fuzz.ClaimedAt is not a valid ISO 8601 timestamp
//   - fuzz.ClaimedAt is before the ticket's CreatedAt
//   - the ticket doesn't exist
//   - the ticket cannot be started (see canStart for details)
func (m *Model) Start(user UserStartInput, fuzz FuzzStartInput) *Error {
	if user.ClaimedBy == "" {
		return newErr(ErrClaimedByRequired)
	}

	claimedAt, err := toISO8601(fuzz.ClaimedAt)
	if err != nil {
		return err
	}

	tk, ok := m.tickets[user.ID]
	if !ok {
		return newErr(ErrTicketNotFound, kv("id", user.ID))
	}

	if claimedAt.Before(tk.CreatedAt) {
		return newErr(ErrStartedBeforeCreated, kv("claimed_at", fuzz.ClaimedAt), kv("created_at", tk.CreatedAt.Format(time.RFC3339)))
	}

	startErr := m.canStart(tk)
	if startErr != nil {
		return startErr
	}

	tk.Status = StatusInProgress
	tk.ClaimedBy = user.ClaimedBy
	tk.ClaimedAt = claimedAt

	return nil
}

// UserCloseInput contains user-provided values for closing a ticket.
type UserCloseInput struct {
	ID string // ticket ID to close
}

// FuzzCloseInput contains values provided by the fuzz test runner for closing a ticket.
type FuzzCloseInput struct {
	ClosedAt string // ISO 8601 timestamp of closure
}

// Close transitions a ticket from StatusInProgress to StatusClosed.
//
// Sets ClosedAt to the provided timestamp.
//
// Returns an error if:
//   - the ticket doesn't exist
//   - the ticket is in StatusOpen (must start first)
//   - the ticket is already in StatusClosed
//   - the ticket has any children that are not closed
//   - fuzz.ClosedAt is not a valid ISO 8601 timestamp
//   - fuzz.ClosedAt is before the ticket's CreatedAt
//
// Panics if the ticket has an unknown status (invalid spec state).
func (m *Model) Close(user UserCloseInput, fuzz FuzzCloseInput) *Error {
	tk, ok := m.tickets[user.ID]
	if !ok {
		return newErr(ErrTicketNotFound, kv("id", user.ID))
	}

	switch tk.Status {
	case StatusOpen:
		return newErr(ErrCantCloseOpen, kv("id", user.ID), kv("status", string(tk.Status)))
	case StatusClosed:
		return newErr(ErrCantCloseClosed, kv("id", user.ID), kv("status", string(tk.Status)))
	case StatusInProgress:
		// ok
	default:
		panic(fmt.Sprintf("invalid spec state: ticket %q has unknown status %q", user.ID, tk.Status))
	}

	if openChild := m.findOpenChild(user.ID); openChild != "" {
		return newErr(ErrHasOpenChildren, kv("id", user.ID), kv("open_child_id", openChild))
	}

	closedAt, err := toISO8601(fuzz.ClosedAt)
	if err != nil {
		return err
	}

	if closedAt.Before(tk.CreatedAt) {
		return newErr(ErrClosedBeforeCreated, kv("closed_at", fuzz.ClosedAt), kv("created_at", tk.CreatedAt.Format(time.RFC3339)))
	}

	tk.ClosedAt = closedAt
	tk.Status = StatusClosed
	tk.ClaimedBy = ""
	tk.ClaimedAt = time.Time{}

	return nil
}

// UserReopenInput contains user-provided values for reopening a ticket.
type UserReopenInput struct {
	ID string // ticket ID to reopen
}

// Reopen transitions a ticket from StatusClosed to StatusOpen.
//
// Clears ClosedAt (sets to zero time).
//
// Returns an error if:
//   - the ticket doesn't exist
//   - the ticket is in StatusOpen
//   - the ticket is in StatusInProgress
//   - the ticket has a parent that is closed (must reopen parent first)
//
// Panics if the ticket has an unknown status (invalid spec state).
func (m *Model) Reopen(user UserReopenInput) *Error {
	tk, ok := m.tickets[user.ID]
	if !ok {
		return newErr(ErrTicketNotFound, kv("id", user.ID))
	}

	switch tk.Status {
	case StatusOpen:
		return newErr(ErrCantReopenOpen, kv("id", user.ID), kv("status", string(tk.Status)))
	case StatusInProgress:
		return newErr(ErrCantReopenInProgress, kv("id", user.ID), kv("status", string(tk.Status)))
	case StatusClosed:
		// ok
	default:
		panic(fmt.Sprintf("invalid spec state: ticket %q has unknown status %q", user.ID, tk.Status))
	}

	if tk.ParentID != "" {
		parent := m.tickets[tk.ParentID]
		if parent.Status == StatusClosed {
			return newErr(ErrParentAlreadyClosed, kv("id", user.ID), kv("parent_id", tk.ParentID))
		}
	}

	tk.Status = StatusOpen
	tk.ClosedAt = time.Time{}

	return nil
}

// UserBlockInput contains user-provided values for blocking a ticket.
type UserBlockInput struct {
	ID        string // ticket ID to add blocker to
	BlockerID string // ticket ID of the blocker
}

// Block adds blockerID to the ticket's BlockedBy list.
//
// Returns an error if:
//   - the ticket doesn't exist
//   - the ticket is closed
//   - user.BlockerID is empty
//   - the blocker ticket doesn't exist
//   - user.ID == user.BlockerID (can't block itself)
//   - the ticket is already blocked by user.BlockerID
//   - adding this blocker would create a cycle (user.ID is in user.BlockerID's blocker chain)
func (m *Model) Block(user UserBlockInput) *Error {
	tk, ok := m.tickets[user.ID]
	if !ok {
		return newErr(ErrTicketNotFound, kv("id", user.ID))
	}

	if tk.Status == StatusClosed {
		return newErr(ErrTicketClosed, kv("id", user.ID))
	}

	if user.BlockerID == "" {
		return newErr(ErrBlockerIDRequired, kv("blocker_id", user.BlockerID))
	}

	if _, ok := m.tickets[user.BlockerID]; !ok {
		return newErr(ErrBlockerNotFound, kv("blocker_id", user.BlockerID))
	}

	if user.ID == user.BlockerID {
		return newErr(ErrCantBlockSelf, kv("id", user.ID))
	}

	if slices.Contains(tk.BlockedBy, user.BlockerID) {
		return newErr(ErrAlreadyBlocked, kv("id", user.ID), kv("blocker_id", user.BlockerID))
	}

	if cyclePath := m.findBlockerCyclePath(user.ID, user.BlockerID); cyclePath != nil {
		return newErr(ErrBlockerCycle, kv("id", user.ID), kv("blocker_id", user.BlockerID), kv("cycle", strings.Join(cyclePath, "→")))
	}

	tk.BlockedBy = append(tk.BlockedBy, user.BlockerID)

	return nil
}

// UserUnblockInput contains user-provided values for unblocking a ticket.
type UserUnblockInput struct {
	ID        string // ticket ID to remove blocker from
	BlockerID string // ticket ID of the blocker to remove
}

// Unblock removes blockerID from the ticket's BlockedBy list.
//
// Returns an error if:
//   - the ticket doesn't exist
//   - the ticket is closed
//   - the ticket is not blocked by user.BlockerID
func (m *Model) Unblock(user UserUnblockInput) *Error {
	tk, ok := m.tickets[user.ID]
	if !ok {
		return newErr(ErrTicketNotFound, kv("id", user.ID))
	}

	if tk.Status == StatusClosed {
		return newErr(ErrTicketClosed, kv("id", user.ID))
	}

	idx := slices.Index(tk.BlockedBy, user.BlockerID)
	if idx == -1 {
		return newErr(ErrNotBlocked, kv("id", user.ID), kv("blocker_id", user.BlockerID))
	}

	tk.BlockedBy = slices.Delete(tk.BlockedBy, idx, idx+1)

	return nil
}

// UserShowInput contains user-provided values for showing a ticket.
type UserShowInput struct {
	ID string // ticket ID to show
}

// Show returns a copy of the ticket state.
//
// The returned Ticket is a deep copy; modifying it does not affect the model.
//
// Returns an error if the ticket doesn't exist.
func (m *Model) Show(user UserShowInput) (Ticket, *Error) {
	tk, ok := m.tickets[user.ID]
	if !ok {
		return Ticket{}, newErr(ErrTicketNotFound, kv("id", user.ID))
	}

	cp := *tk
	cp.BlockedBy = slices.Clone(tk.BlockedBy)

	return cp, nil
}

// IDs returns all known ticket IDs in creation order.
//
// Returns a copy of the order slice; modifying it does not affect the model.
func (m *Model) IDs() []string {
	return slices.Clone(m.order)
}

// UserLSInput configures the LS query.
//
// All fields are optional:
//   - Status: filter by status; empty string means all statuses
//   - Priority: filter by priority; 0 means all priorities
//   - Type: filter by type; empty string means all types
//   - Offset: skip the first N results; must be >= 0
//   - Limit: return at most N results; 0 means unlimited; must be >= 0
type UserLSInput struct {
	Status   string // empty = all statuses
	Priority int    // 0 = all priorities
	Type     string // empty = all types
	Offset   int    // must be >= 0
	Limit    int    // must be >= 0; 0 = unlimited
}

// LS returns tickets matching the given options in creation order.
//
// Filters are applied as AND (all must match), then offset, then limit.
//
// Returns deep copies of the tickets; modifying them does not affect the model.
//
// Returns an error if:
//   - Status is non-empty but not a valid Status value
//   - Priority is non-zero but not in range 1-4
//   - Type is non-empty but not a valid Type value
//   - Offset is negative
//   - Limit is negative
//   - Offset exceeds total matching results (makes bugs visible)
func (m *Model) LS(user UserLSInput) ([]Ticket, *Error) {
	// Validate filters
	var statusFilter Status

	if user.Status != "" {
		s, err := toStatus(user.Status)
		if err != nil {
			return nil, err
		}

		statusFilter = s
	}

	var priorityFilter Priority

	if user.Priority != 0 {
		p, err := toPriority(user.Priority)
		if err != nil {
			return nil, err
		}

		priorityFilter = p
	}

	var typeFilter Type

	if user.Type != "" {
		t, err := toType(user.Type)
		if err != nil {
			return nil, err
		}

		typeFilter = t
	}

	if user.Offset < 0 {
		return nil, newErr(ErrInvalidOffset, kv("offset", strconv.Itoa(user.Offset)))
	}

	if user.Limit < 0 {
		return nil, newErr(ErrInvalidLimit, kv("limit", strconv.Itoa(user.Limit)))
	}

	// Filter
	var tickets []Ticket
	for _, id := range m.order {
		tk := m.tickets[id]
		if statusFilter != "" && tk.Status != statusFilter {
			continue
		}

		if priorityFilter != 0 && tk.Priority != priorityFilter {
			continue
		}

		if typeFilter != "" && tk.Type != typeFilter {
			continue
		}

		cp := *tk
		cp.BlockedBy = slices.Clone(tk.BlockedBy)
		tickets = append(tickets, cp)
	}

	// Apply offset
	if user.Offset > 0 && user.Offset >= len(tickets) {
		return nil, newErr(ErrOffsetOutOfBounds, kv("offset", strconv.Itoa(user.Offset)), kv("count", strconv.Itoa(len(tickets))))
	}

	tickets = tickets[user.Offset:]

	// Apply limit
	if user.Limit > 0 && user.Limit < len(tickets) {
		tickets = tickets[:user.Limit]
	}

	return tickets, nil
}

// UserReadyInput configures the Ready query.
//
// All fields are optional:
//   - Offset: skip the first N results; must be >= 0
//   - Limit: return at most N results; 0 means unlimited; must be >= 0
type UserReadyInput struct {
	Offset int // must be >= 0
	Limit  int // must be >= 0; 0 = unlimited
}

// Ready returns tickets that are ready to be worked on.
//
// A ticket is "ready" if it can be started. See [Model.Start] for the conditions.
//
// Results are sorted by priority (ascending), then by creation order.
// This uses a stable sort, so tickets with equal priority maintain their creation order.
//
// Returns deep copies of the tickets; modifying them does not affect the model.
//
// Returns an error if:
//   - Offset is negative
//   - Limit is negative
//   - Offset exceeds total matching results
func (m *Model) Ready(user UserReadyInput) ([]Ticket, *Error) {
	if user.Offset < 0 {
		return nil, newErr(ErrInvalidOffset, kv("offset", strconv.Itoa(user.Offset)))
	}

	if user.Limit < 0 {
		return nil, newErr(ErrInvalidLimit, kv("limit", strconv.Itoa(user.Limit)))
	}

	var ready []Ticket

	for _, id := range m.order {
		tk := m.tickets[id]

		if m.canStart(tk) != nil {
			continue
		}

		cp := *tk
		cp.BlockedBy = slices.Clone(tk.BlockedBy)
		ready = append(ready, cp)
	}

	slices.SortStableFunc(ready, func(a, b Ticket) int {
		return int(a.Priority - b.Priority)
	})

	// Apply offset
	if user.Offset > 0 && user.Offset >= len(ready) {
		return nil, newErr(ErrOffsetOutOfBounds, kv("offset", strconv.Itoa(user.Offset)), kv("count", strconv.Itoa(len(ready))))
	}

	ready = ready[user.Offset:]

	// Apply limit
	if user.Limit > 0 && user.Limit < len(ready) {
		ready = ready[:user.Limit]
	}

	return ready, nil
}

// findOpenChild returns the ID of the first child of parentID that is not closed.
// Returns empty string if all children are closed (or there are no children).
func (m *Model) findOpenChild(parentID string) string {
	for _, id := range m.order {
		tk := m.tickets[id]
		if tk.ParentID == parentID && tk.Status != StatusClosed {
			return id
		}
	}

	return ""
}

// findBlockerCyclePath checks if adding "id blocked by blockerID" would create a cycle.
// If a cycle would be created, returns the path that forms the cycle (e.g. ["A", "B", "C", "A"]).
// Returns nil if no cycle would be created.
func (m *Model) findBlockerCyclePath(id, blockerID string) []string {
	visited := make(map[string]bool)

	path := m.findPathTo(blockerID, id, visited)
	if path != nil {
		// path is [blockerID, ..., id], add id at start to show full cycle
		return append([]string{id}, path...)
	}

	return nil
}

// findPathTo searches for a path from "from" to "target" through the blocker graph.
// Returns the path including both endpoints, or nil if no path exists.
func (m *Model) findPathTo(from, target string, visited map[string]bool) []string {
	if visited[from] {
		return nil
	}

	visited[from] = true

	if from == target {
		return []string{target}
	}

	tk := m.tickets[from]
	for _, bid := range tk.BlockedBy {
		if path := m.findPathTo(bid, target, visited); path != nil {
			return append([]string{from}, path...)
		}
	}

	return nil
}

// canStart checks if a ticket can be started.
// Returns an error if:
//   - ticket is not Open
//   - ticket has open blockers
//   - parent is Open (must start parent first)
//   - any ancestor has open blockers (ancestor status doesn't matter once parent is started)
func (m *Model) canStart(tk *Ticket) *Error {
	if tk.Status != StatusOpen {
		return newErr(ErrCantStartNotOpen, kv("id", tk.ID), kv("status", string(tk.Status)))
	}

	// Own blockers must all be closed
	if blocker := m.findOpenBlocker(tk); blocker != "" {
		return newErr(ErrHasOpenBlockers, kv("id", tk.ID), kv("blocker_id", blocker))
	}

	// No parent = can start
	if tk.ParentID == "" {
		return nil
	}

	parent := m.tickets[tk.ParentID]

	// Parent must be started (not Open)
	if parent.Status == StatusOpen {
		return newErr(ErrParentNotStarted, kv("id", tk.ID), kv("parent_id", tk.ParentID))
	}

	// Once parent is started or closed, ensure ancestors are unblocked.
	if ancestor := m.findAncestorWithOpenBlocker(parent, make(map[string]bool)); ancestor != nil {
		return newErr(ErrAncestorNotReady, kv("id", tk.ID), kv("ancestor_id", ancestor.ID))
	}

	return nil
}

// findAncestorWithOpenBlocker returns the first ancestor (including start node)
// that has an open blocker. Status is ignored here; only blockers matter.
func (m *Model) findAncestorWithOpenBlocker(tk *Ticket, visited map[string]bool) *Ticket {
	if visited[tk.ID] {
		return tk
	}

	visited[tk.ID] = true

	if blocker := m.findOpenBlocker(tk); blocker != "" {
		return tk
	}

	if tk.ParentID == "" {
		return nil
	}

	parent := m.tickets[tk.ParentID]

	return m.findAncestorWithOpenBlocker(parent, visited)
}

// findOpenBlocker returns the ID of the first blocker that is not closed.
// Returns empty string if all blockers are closed.
func (m *Model) findOpenBlocker(tk *Ticket) string {
	for _, blockerID := range tk.BlockedBy {
		blocker, ok := m.tickets[blockerID]
		if !ok {
			panic(fmt.Sprintf("invalid spec state: ticket %q has unknown blocker %q", tk.ID, blockerID))
		}

		if blocker.Status != StatusClosed {
			return blockerID
		}
	}

	return ""
}

func toType(t string) (Type, *Error) {
	switch Type(t) {
	case TypeBug, TypeFeature, TypeTask, TypeEpic, TypeChore:
		return Type(t), nil
	default:
		return "", newErr(ErrInvalidType, kv("type", t))
	}
}

func toPriority(p int) (Priority, *Error) {
	pr := Priority(p)
	if pr < PriorityCritical || pr > PriorityLow {
		return 0, newErr(ErrInvalidPriority, kv("priority", strconv.Itoa(p)))
	}

	return pr, nil
}

func toStatus(s string) (Status, *Error) {
	st := Status(s)
	if st != StatusOpen && st != StatusInProgress && st != StatusClosed {
		return "", newErr(ErrInvalidStatus, kv("status", s))
	}

	return st, nil
}

func toISO8601(s string) (time.Time, *Error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, newErr(ErrInvalidTimestamp, kv("timestamp", s))
	}

	return t, nil
}
