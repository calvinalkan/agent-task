package cli_test

import (
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"strings"

	"github.com/calvinalkan/agent-task/internal/cli"
)

// TicketModel represents the expected state of a single ticket.
// Intentionally simple so correctness is obvious.
type TicketModel struct {
	ID          string
	Title       string
	Description string
	Design      string
	Acceptance  string
	Type        string
	Priority    int
	Assignee    string
	Status      string // open, in_progress, closed
	BlockedBy   []string
	HasClosed   bool // has closed timestamp
}

// Model tracks all tickets and their expected state.
type Model struct {
	tickets map[string]*TicketModel
	order   []string // track creation order
}

// NewModel creates a new empty model.
func NewModel() *Model {
	return &Model{
		tickets: make(map[string]*TicketModel),
	}
}

// Model errors - match the real system's errors.
var (
	errModelTitleRequired    = errors.New("title is required")
	errModelTicketNotFound   = errors.New("ticket not found")
	errModelTicketNotOpen    = errors.New("ticket is not open")
	errModelTicketNotStarted = errors.New("ticket must be started first")
	errModelAlreadyClosed    = errors.New("ticket is already closed")
	errModelNotClosed        = errors.New("ticket is not closed")
	errModelAlreadyOpen      = errors.New("ticket is already open")
	errModelBlockerNotFound  = errors.New("blocker not found")
	errModelCannotBlockSelf  = errors.New("ticket cannot block itself")
	errModelAlreadyBlockedBy = errors.New("already blocked by")
	errModelNotBlockedBy     = errors.New("not blocked by")
	errModelInvalidType      = errors.New("invalid type")
	errModelInvalidPriority  = errors.New("invalid priority")
)

// CreateOpts holds all the optional arguments for create.
type CreateOpts struct {
	Title       string
	Description string
	Design      string
	Acceptance  string
	Type        string
	Priority    int
	Assignee    string
	BlockedBy   []string
}

// Create adds a new ticket to the model.
// The id parameter is the real ID from the CLI (passed after CLI creates the ticket).
// If id is empty, only validation is performed (for when CLI failed).
func (m *Model) Create(id string, opts CreateOpts) error {
	// Validation first
	if opts.Title == "" {
		return errModelTitleRequired
	}

	if opts.Type != "" {
		validTypes := []string{"bug", "feature", "task", "epic", "chore"}
		if !slices.Contains(validTypes, opts.Type) {
			return errModelInvalidType
		}
	}

	if opts.Priority != 0 {
		if opts.Priority < 1 || opts.Priority > 4 {
			return errModelInvalidPriority
		}
	}

	// Check blockers exist
	for _, blockerID := range opts.BlockedBy {
		if _, ok := m.tickets[blockerID]; !ok {
			return errModelBlockerNotFound
		}
	}

	// If no ID, validation passed but CLI failed - return error
	if id == "" {
		return errors.New("validation passed but CLI failed")
	}

	m.order = append(m.order, id)

	// Apply defaults
	ticketType := opts.Type
	if ticketType == "" {
		ticketType = "task"
	}

	priority := opts.Priority
	if priority == 0 {
		priority = 2
	}

	m.tickets[id] = &TicketModel{
		ID:          id,
		Title:       opts.Title,
		Description: opts.Description,
		Design:      opts.Design,
		Acceptance:  opts.Acceptance,
		Type:        ticketType,
		Priority:    priority,
		Assignee:    opts.Assignee,
		Status:      "open",
		BlockedBy:   slices.Clone(opts.BlockedBy),
		HasClosed:   false,
	}

	return nil
}

// Start transitions a ticket from open to in_progress.
func (m *Model) Start(id string) error {
	tk, ok := m.tickets[id]
	if !ok {
		return errModelTicketNotFound
	}

	if tk.Status != "open" {
		return fmt.Errorf("%w (current status: %s)", errModelTicketNotOpen, tk.Status)
	}

	tk.Status = "in_progress"

	return nil
}

// Close transitions a ticket from in_progress to closed.
func (m *Model) Close(id string) error {
	tk, ok := m.tickets[id]
	if !ok {
		return errModelTicketNotFound
	}

	if tk.Status == "closed" {
		return errModelAlreadyClosed
	}

	if tk.Status != "in_progress" {
		return errModelTicketNotStarted
	}

	tk.Status = "closed"
	tk.HasClosed = true

	return nil
}

// Reopen transitions a ticket from closed to open.
func (m *Model) Reopen(id string) error {
	tk, ok := m.tickets[id]
	if !ok {
		return errModelTicketNotFound
	}

	if tk.Status == "open" {
		return errModelAlreadyOpen
	}

	if tk.Status != "closed" {
		return fmt.Errorf("%w (current status: %s)", errModelNotClosed, tk.Status)
	}

	tk.Status = "open"
	tk.HasClosed = false

	return nil
}

// Block adds a blocker to a ticket.
func (m *Model) Block(id, blockerID string) error {
	tk, ok := m.tickets[id]
	if !ok {
		return errModelTicketNotFound
	}

	if _, ok := m.tickets[blockerID]; !ok {
		return errModelBlockerNotFound
	}

	if id == blockerID {
		return errModelCannotBlockSelf
	}

	if slices.Contains(tk.BlockedBy, blockerID) {
		return fmt.Errorf("%w %s", errModelAlreadyBlockedBy, blockerID)
	}

	tk.BlockedBy = append(tk.BlockedBy, blockerID)

	return nil
}

// Unblock removes a blocker from a ticket.
func (m *Model) Unblock(id, blockerID string) error {
	tk, ok := m.tickets[id]
	if !ok {
		return errModelTicketNotFound
	}

	idx := slices.Index(tk.BlockedBy, blockerID)
	if idx == -1 {
		return fmt.Errorf("%w %s", errModelNotBlockedBy, blockerID)
	}

	tk.BlockedBy = slices.Delete(tk.BlockedBy, idx, idx+1)

	return nil
}

// List returns all ticket IDs.
func (m *Model) List() []string {
	return slices.Clone(m.order)
}

// ListByStatus returns ticket IDs filtered by status.
func (m *Model) ListByStatus(status string) []string {
	var ids []string

	for _, id := range m.order {
		if m.tickets[id].Status == status {
			ids = append(ids, id)
		}
	}

	return ids
}

// Get returns a ticket by ID.
func (m *Model) Get(id string) (*TicketModel, error) {
	tk, ok := m.tickets[id]
	if !ok {
		return nil, errModelTicketNotFound
	}

	return tk, nil
}

// TicketCount returns the number of tickets.
func (m *Model) TicketCount() int {
	return len(m.tickets)
}

// Op represents an operation that can be applied to both model and real CLI.
type Op interface {
	// Apply applies the operation to the model.
	// For most operations, this is called after Run.
	Apply(*Model) error
	// Run executes the operation against the real CLI.
	// Returns (createdID, error) - createdID is only set for Create operations.
	Run(*cli.CLI) (createdID string, err error)
	String() string
}

// CreateOp creates a new ticket.
type CreateOp struct {
	Opts      CreateOpts
	CreatedID string // Set after Run() succeeds, used by Apply()
}

func (o *CreateOp) Apply(m *Model) error {
	return m.Create(o.CreatedID, o.Opts)
}

func (o *CreateOp) Run(c *cli.CLI) (string, error) {
	args := []string{"create", o.Opts.Title}

	if o.Opts.Description != "" {
		args = append(args, "-d", o.Opts.Description)
	}

	if o.Opts.Design != "" {
		args = append(args, "--design", o.Opts.Design)
	}

	if o.Opts.Acceptance != "" {
		args = append(args, "--acceptance", o.Opts.Acceptance)
	}

	if o.Opts.Type != "" {
		args = append(args, "-t", o.Opts.Type)
	}

	if o.Opts.Priority != 0 {
		args = append(args, "-p", strconv.Itoa(o.Opts.Priority))
	}

	if o.Opts.Assignee != "" {
		args = append(args, "-a", o.Opts.Assignee)
	}

	for _, b := range o.Opts.BlockedBy {
		args = append(args, "--blocked-by", b)
	}

	stdout, stderr, code := c.Run(args...)
	if code != 0 {
		return "", errors.New(stderr)
	}
	// Extract created ID from stdout (first line)
	id := strings.TrimSpace(strings.Split(stdout, "\n")[0])
	o.CreatedID = id

	return id, nil
}

func (o *CreateOp) String() string {
	return fmt.Sprintf("Create(%q)", o.Opts.Title)
}

// StartOp starts a ticket.
type StartOp struct {
	ID string
}

func (o StartOp) Apply(m *Model) error {
	return m.Start(o.ID)
}

func (o StartOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("start", o.ID)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o StartOp) String() string {
	return fmt.Sprintf("Start(%s)", o.ID)
}

// CloseOp closes a ticket.
type CloseOp struct {
	ID string
}

func (o CloseOp) Apply(m *Model) error {
	return m.Close(o.ID)
}

func (o CloseOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("close", o.ID)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o CloseOp) String() string {
	return fmt.Sprintf("Close(%s)", o.ID)
}

// ReopenOp reopens a ticket.
type ReopenOp struct {
	ID string
}

func (o ReopenOp) Apply(m *Model) error {
	return m.Reopen(o.ID)
}

func (o ReopenOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("reopen", o.ID)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o ReopenOp) String() string {
	return fmt.Sprintf("Reopen(%s)", o.ID)
}

// BlockOp blocks a ticket.
type BlockOp struct {
	ID string
	By string
}

func (o BlockOp) Apply(m *Model) error {
	return m.Block(o.ID, o.By)
}

func (o BlockOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("block", o.ID, o.By)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o BlockOp) String() string {
	return fmt.Sprintf("Block(%s, %s)", o.ID, o.By)
}

// UnblockOp unblocks a ticket.
type UnblockOp struct {
	ID string
	By string
}

func (o UnblockOp) Apply(m *Model) error {
	return m.Unblock(o.ID, o.By)
}

func (o UnblockOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("unblock", o.ID, o.By)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o UnblockOp) String() string {
	return fmt.Sprintf("Unblock(%s, %s)", o.ID, o.By)
}

// ListOp lists all tickets.
type ListOp struct{}

func (o ListOp) Apply(m *Model) error {
	_ = m.List()

	return nil
}

func (o ListOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("ls")
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o ListOp) String() string {
	return "List()"
}

// ListByStatusOp lists tickets by status.
type ListByStatusOp struct {
	Status string
}

func (o ListByStatusOp) Apply(m *Model) error {
	_ = m.ListByStatus(o.Status)

	return nil
}

func (o ListByStatusOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("ls", "--status", o.Status)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o ListByStatusOp) String() string {
	return fmt.Sprintf("ListByStatus(%s)", o.Status)
}

// ShowOp shows a ticket.
type ShowOp struct {
	ID string
}

func (o ShowOp) Apply(m *Model) error {
	_, err := m.Get(o.ID)

	return err
}

func (o ShowOp) Run(c *cli.CLI) (string, error) {
	_, stderr, code := c.Run("show", o.ID)
	if code != 0 {
		return "", errors.New(stderr)
	}

	return "", nil
}

func (o ShowOp) String() string {
	return fmt.Sprintf("Show(%s)", o.ID)
}

// genOp generates a random operation based on current model state.
func genOp(rng *rand.Rand, model *Model, realIDs []string) Op {
	switch rng.Intn(10) {
	case 0, 1:
		return &CreateOp{Opts: genCreateOpts(rng, realIDs)}
	case 2:
		return StartOp{ID: pickTicketID(rng, realIDs)}
	case 3:
		return CloseOp{ID: pickTicketID(rng, realIDs)}
	case 4:
		return ReopenOp{ID: pickTicketID(rng, realIDs)}
	case 5:
		return BlockOp{ID: pickTicketID(rng, realIDs), By: pickTicketID(rng, realIDs)}
	case 6:
		return UnblockOp{ID: pickTicketID(rng, realIDs), By: pickTicketID(rng, realIDs)}
	case 7:
		return ListOp{}
	case 8:
		statuses := []string{"open", "in_progress", "closed"}

		return ListByStatusOp{Status: statuses[rng.Intn(len(statuses))]}
	case 9:
		return ShowOp{ID: pickTicketID(rng, realIDs)}
	}

	return ListOp{}
}

// genCreateOpts generates random create options.
func genCreateOpts(rng *rand.Rand, existingIDs []string) CreateOpts {
	opts := CreateOpts{
		Title: pickTitle(rng),
	}

	// 30% chance of description
	if rng.Float32() < 0.3 {
		opts.Description = pickDescription(rng)
	}

	// 20% chance of design
	if rng.Float32() < 0.2 {
		opts.Design = "Design notes for this ticket"
	}

	// 20% chance of acceptance
	if rng.Float32() < 0.2 {
		opts.Acceptance = "All tests must pass"
	}

	// 50% chance of custom type
	if rng.Float32() < 0.5 {
		types := []string{"bug", "feature", "task", "epic", "chore"}
		opts.Type = types[rng.Intn(len(types))]
	}

	// 50% chance of custom priority
	if rng.Float32() < 0.5 {
		opts.Priority = rng.Intn(4) + 1 // 1-4
	}

	// 30% chance of assignee
	if rng.Float32() < 0.3 {
		opts.Assignee = pickAssignee(rng)
	}

	// 20% chance of blockers (if tickets exist)
	if rng.Float32() < 0.2 && len(existingIDs) > 0 {
		numBlockers := rng.Intn(2) + 1
		for i := 0; i < numBlockers && i < len(existingIDs); i++ {
			opts.BlockedBy = append(opts.BlockedBy, existingIDs[rng.Intn(len(existingIDs))])
		}
	}

	return opts
}

// pickTitle returns a random title, biased towards edge cases.
func pickTitle(rng *rand.Rand) string {
	titles := []string{
		"",                       // empty - should fail
		"Fix bug",                // simple
		"Add new feature",        // normal
		"Refactor the code base", // longer
		"A",                      // minimal
	}

	return titles[rng.Intn(len(titles))]
}

// pickDescription returns a random description.
func pickDescription(rng *rand.Rand) string {
	descriptions := []string{
		"This is a description",
		"Short",
		"A longer description with more details about what needs to be done",
	}

	return descriptions[rng.Intn(len(descriptions))]
}

// pickAssignee returns a random assignee.
func pickAssignee(rng *rand.Rand) string {
	assignees := []string{"Alice", "Bob", "Charlie", "Diana"}

	return assignees[rng.Intn(len(assignees))]
}

// pickTicketID returns a random existing ID, or invalid ID sometimes.
func pickTicketID(rng *rand.Rand, existingIDs []string) string {
	// 20% chance of invalid ID
	if rng.Float32() < 0.2 || len(existingIDs) == 0 {
		invalids := []string{"", "nonexistent", "INVALID-999"}

		return invalids[rng.Intn(len(invalids))]
	}

	return existingIDs[rng.Intn(len(existingIDs))]
}
