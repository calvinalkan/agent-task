package testutil

import (
	"slices"

	"github.com/calvinalkan/agent-task/internal/testutil/spec"
)

// OpGenConfig configures the operation generator.
type OpGenConfig struct {
	// CreateRate is the percentage of ops that create tickets (0-100).
	CreateRate int

	// StartRate is the percentage of ops that start tickets.
	StartRate int

	// CloseRate is the percentage of ops that close tickets.
	CloseRate int

	// ReopenRate is the percentage of ops that reopen tickets.
	ReopenRate int

	// BlockRate is the percentage of ops that block tickets.
	BlockRate int

	// UnblockRate is the percentage of ops that unblock tickets.
	UnblockRate int

	// ShowRate is the percentage of ops that show tickets.
	ShowRate int

	// LSRate is the percentage of ops that list tickets.
	LSRate int

	// ReadyRate is the percentage of ops that list ready tickets.
	ReadyRate int

	// InvalidIDRate is the percentage of ID references that use invalid IDs.
	InvalidIDRate int

	// InvalidInputRate is the percentage of inputs that use invalid values.
	InvalidInputRate int
}

// DefaultOpGenConfig returns a balanced configuration.
func DefaultOpGenConfig() OpGenConfig {
	return OpGenConfig{
		CreateRate:       20,
		StartRate:        15,
		CloseRate:        10,
		ReopenRate:       5,
		BlockRate:        10,
		UnblockRate:      5,
		ShowRate:         10,
		LSRate:           15,
		ReadyRate:        10,
		InvalidIDRate:    15,
		InvalidInputRate: 10,
	}
}

// OpGenerator generates deterministic operations from a byte stream.
type OpGenerator struct {
	stream *ByteStream
	config OpGenConfig
	model  *spec.Model
}

// NewOpGenerator creates a new operation generator.
func NewOpGenerator(fuzzBytes []byte, model *spec.Model, cfg *OpGenConfig) *OpGenerator {
	return &OpGenerator{
		stream: NewByteStream(fuzzBytes),
		config: *cfg,
		model:  model,
	}
}

// HasMore reports whether more operations can be generated.
func (g *OpGenerator) HasMore() bool {
	return g.stream.HasMore()
}

// NextOp generates the next operation.
func (g *OpGenerator) NextOp() Op {
	ids := g.model.IDs()

	// Choose operation type based on configured rates
	choice := int(g.stream.NextByte()) % 100

	cumulative := 0

	cumulative += g.config.CreateRate
	if choice < cumulative {
		return g.genCreate(ids)
	}

	cumulative += g.config.StartRate
	if choice < cumulative {
		return g.genStart(ids)
	}

	cumulative += g.config.CloseRate
	if choice < cumulative {
		return g.genClose(ids)
	}

	cumulative += g.config.ReopenRate
	if choice < cumulative {
		return g.genReopen(ids)
	}

	cumulative += g.config.BlockRate
	if choice < cumulative {
		return g.genBlock(ids)
	}

	cumulative += g.config.UnblockRate
	if choice < cumulative {
		return g.genUnblock(ids)
	}

	cumulative += g.config.ShowRate
	if choice < cumulative {
		return g.genShow(ids)
	}

	cumulative += g.config.LSRate
	if choice < cumulative {
		return g.genLS()
	}

	return g.genReady()
}

func (g *OpGenerator) genCreate(existingIDs []string) Op {
	op := &OpCreate{}

	// Title - sometimes empty to test validation
	if g.shouldBeInvalid() {
		op.Title = ""
	} else {
		op.Title = g.genTitle()
	}

	// Description - optional
	if g.stream.NextBool() {
		op.Description = g.stream.NextString(50)
	}

	// Type - sometimes invalid
	if g.stream.NextBool() {
		if g.shouldBeInvalid() {
			op.Type = "invalid_type"
		} else {
			types := []string{"bug", "feature", "task", "epic", "chore"}
			op.Type = types[g.stream.NextInt(len(types))]
		}
	}

	// Priority - sometimes invalid
	if g.stream.NextBool() {
		if g.shouldBeInvalid() {
			op.Priority = 5 + g.stream.NextInt(10) // out of range
		} else {
			op.Priority = 1 + g.stream.NextInt(4)
		}
	}

	// Parent - sometimes invalid
	if g.stream.NextBool() && len(existingIDs) > 0 {
		if g.shouldUseInvalidID() {
			op.ParentID = "nonexistent"
		} else {
			op.ParentID = existingIDs[g.stream.NextInt(len(existingIDs))]
		}
	}

	// Blockers
	if g.stream.NextBool() && len(existingIDs) > 0 {
		numBlockers := 1 + g.stream.NextInt(2)
		for range numBlockers {
			if g.shouldUseInvalidID() {
				op.BlockedBy = append(op.BlockedBy, "nonexistent")
			} else if len(existingIDs) > 0 {
				op.BlockedBy = append(op.BlockedBy, existingIDs[g.stream.NextInt(len(existingIDs))])
			}
		}
	}

	return op
}

func (g *OpGenerator) genStart(ids []string) Op {
	return OpStart{ID: g.pickID(ids)}
}

func (g *OpGenerator) genClose(ids []string) Op {
	return OpClose{ID: g.pickID(ids)}
}

func (g *OpGenerator) genReopen(ids []string) Op {
	return OpReopen{ID: g.pickID(ids)}
}

func (g *OpGenerator) genBlock(ids []string) Op {
	return OpBlock{
		ID:        g.pickID(ids),
		BlockerID: g.pickID(ids),
	}
}

func (g *OpGenerator) genUnblock(ids []string) Op {
	return OpUnblock{
		ID:        g.pickID(ids),
		BlockerID: g.pickID(ids),
	}
}

func (g *OpGenerator) genShow(ids []string) Op {
	return OpShow{ID: g.pickID(ids)}
}

func (g *OpGenerator) genLS() Op {
	op := OpLS{}

	// Status filter
	if g.stream.NextBool() {
		if g.shouldBeInvalid() {
			op.Status = "invalid"
		} else {
			statuses := []string{"open", "in_progress", "closed"}
			op.Status = statuses[g.stream.NextInt(len(statuses))]
		}
	}

	// Priority filter
	if g.stream.NextBool() {
		if g.shouldBeInvalid() {
			op.Priority = 10
		} else {
			op.Priority = 1 + g.stream.NextInt(4)
		}
	}

	// Type filter
	if g.stream.NextBool() {
		if g.shouldBeInvalid() {
			op.Type = "invalid"
		} else {
			types := []string{"bug", "feature", "task", "epic", "chore"}
			op.Type = types[g.stream.NextInt(len(types))]
		}
	}

	return op
}

func (g *OpGenerator) genReady() Op {
	op := OpReady{}

	if g.stream.NextBool() {
		op.Limit = g.stream.NextInt(10)
	}

	return op
}

func (g *OpGenerator) genTitle() string {
	titles := []string{
		"Fix bug",
		"Add feature",
		"Refactor code",
		"Update docs",
		"Write tests",
		"Review PR",
		"Deploy app",
		"Fix typo",
		"Add logging",
		"Improve perf",
	}

	return titles[g.stream.NextInt(len(titles))]
}

func (g *OpGenerator) pickID(ids []string) string {
	if len(ids) == 0 || g.shouldUseInvalidID() {
		invalids := []string{"", "nonexistent", "INVALID-999"}

		return invalids[g.stream.NextInt(len(invalids))]
	}

	return ids[g.stream.NextInt(len(ids))]
}

func (g *OpGenerator) shouldBeInvalid() bool {
	return int(g.stream.NextByte())%100 < g.config.InvalidInputRate
}

func (g *OpGenerator) shouldUseInvalidID() bool {
	return int(g.stream.NextByte())%100 < g.config.InvalidIDRate
}

// TicketsWithStatus returns IDs of tickets with the given status.
func TicketsWithStatus(m *spec.Model, status string) []string {
	var ids []string

	for _, id := range m.IDs() {
		tk, err := m.Show(spec.UserShowInput{ID: id})
		if err == nil && string(tk.Status) == status {
			ids = append(ids, id)
		}
	}

	return ids
}

// ReadyTickets returns IDs of tickets that are ready to start.
func ReadyTickets(m *spec.Model) []string {
	tickets, err := m.Ready(spec.UserReadyInput{})
	if err != nil {
		return nil
	}

	ids := make([]string, len(tickets))
	for i := range tickets {
		ids[i] = tickets[i].ID
	}

	return ids
}

// TicketsWithBlocker returns IDs of tickets blocked by the given blocker.
func TicketsWithBlocker(m *spec.Model, blockerID string) []string {
	var ids []string

	for _, id := range m.IDs() {
		tk, err := m.Show(spec.UserShowInput{ID: id})
		if err == nil && slices.Contains(tk.BlockedBy, blockerID) {
			ids = append(ids, id)
		}
	}

	return ids
}
