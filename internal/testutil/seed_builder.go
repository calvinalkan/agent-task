package testutil

import "fmt"

// SeedBuilder builds deterministic byte seeds for OpGenerator without
// hand-writing raw byte sequences.
//
// The builder encodes values according to OpGenerator's byte consumption order.
// It automatically tracks created ticket IDs (T0, T1, T2...) so subsequent
// operations can reference them by name.
type SeedBuilder struct {
	cfg       OpGenConfig
	knownIDs  []string
	nextIDNum int
	data      []byte
}

// NewSeedBuilder creates a new builder for the given OpGenerator config.
func NewSeedBuilder(cfg *OpGenConfig) *SeedBuilder {
	if cfg == nil {
		panic("seed builder: cfg must not be nil")
	}

	return &SeedBuilder{cfg: *cfg}
}

// WithKnownIDs pre-populates the builder with known ticket IDs.
//
// This is useful when the model is already seeded with IDs and you want
// to reference them without emitting Create operations.
func (b *SeedBuilder) WithKnownIDs(ids ...string) *SeedBuilder {
	b.knownIDs = append([]string(nil), ids...)
	b.nextIDNum = len(b.knownIDs)

	return b
}

// Bytes returns a copy of the built seed bytes.
func (b *SeedBuilder) Bytes() []byte {
	return append([]byte(nil), b.data...)
}

// -----------------------------------------------------------------------------
// High-level ops (recommended API)
// -----------------------------------------------------------------------------

// CreateArgs configures the Create operation encoding.
type CreateArgs struct {
	Title     string
	Type      string
	Priority  int
	ParentID  string
	BlockedBy []string
}

// Create appends bytes for a create operation with the provided arguments.
// The builder auto-assigns the next ID (T0, T1, T2...) for later references.
func (b *SeedBuilder) Create(args *CreateArgs) *SeedBuilder {
	// Auto-assign ID for this ticket
	_ = b.nextID()

	b.opCreate()

	// Title.
	if args.Title == "" {
		b.inputInvalid()
	} else {
		idx := indexOf(seedTitles, args.Title)
		if idx < 0 {
			panic(fmt.Sprintf("seed builder: unknown title %q", args.Title))
		}

		b.inputValid()
		b.appendInt(idx)
	}

	// Description: not supported yet (always none).
	b.appendBool(false)

	// Type.
	if args.Type == "" {
		b.appendBool(false)
	} else {
		idx := indexOf(seedTypes, args.Type)
		if idx < 0 {
			panic(fmt.Sprintf("seed builder: unknown type %q", args.Type))
		}

		b.appendBool(true)
		b.inputValid()
		b.appendInt(idx)
	}

	// Priority.
	if args.Priority == 0 {
		b.appendBool(false)
	} else {
		if args.Priority < 1 || args.Priority > 4 {
			panic(fmt.Sprintf("seed builder: invalid priority %d", args.Priority))
		}

		b.appendBool(true)
		b.inputValid()
		b.appendInt(args.Priority - 1)
	}

	// Parent - reference must be to an already-created ticket.
	if args.ParentID == "" {
		b.appendBool(false)
	} else {
		// Parent must reference a previously created ticket (not this one).
		idx := indexOf(b.knownIDs[:len(b.knownIDs)-1], args.ParentID)
		if idx < 0 {
			panic(fmt.Sprintf("seed builder: unknown parent ID %q (known=%v)", args.ParentID, b.knownIDs[:len(b.knownIDs)-1]))
		}

		b.appendBool(true)
		b.idValid()
		b.appendInt(idx)
	}

	// Blockers - must reference already-created tickets.
	if len(args.BlockedBy) == 0 {
		b.appendBool(false)

		return b
	}

	if len(args.BlockedBy) > 2 {
		panic("seed builder: Create supports at most 2 blockers")
	}

	b.appendBool(true)
	b.appendInt(len(args.BlockedBy) - 1) // 0 => 1 blocker, 1 => 2 blockers

	for _, blockerID := range args.BlockedBy {
		idx := indexOf(b.knownIDs[:len(b.knownIDs)-1], blockerID)
		if idx < 0 {
			panic(fmt.Sprintf("seed builder: unknown blocker ID %q (known=%v)", blockerID, b.knownIDs[:len(b.knownIDs)-1]))
		}

		b.idValid()
		b.appendInt(idx)
	}

	return b
}

// Start appends bytes for a start operation on a valid known ID.
func (b *SeedBuilder) Start(id string) *SeedBuilder {
	b.opStart()
	b.pickID(id)

	return b
}

// StartInvalid appends bytes for a start operation on an invalid ID.
func (b *SeedBuilder) StartInvalid(id string) *SeedBuilder {
	b.opStart()
	b.pickInvalidID(id)

	return b
}

// Close appends bytes for a close operation.
func (b *SeedBuilder) Close(id string) *SeedBuilder {
	b.opClose()
	b.pickID(id)

	return b
}

// Reopen appends bytes for a reopen operation.
func (b *SeedBuilder) Reopen(id string) *SeedBuilder {
	b.opReopen()
	b.pickID(id)

	return b
}

// Block appends bytes for a block operation.
func (b *SeedBuilder) Block(id, blockerID string) *SeedBuilder {
	b.opBlock()
	b.pickID(id)
	b.pickID(blockerID)

	return b
}

// Unblock appends bytes for an unblock operation.
func (b *SeedBuilder) Unblock(id, blockerID string) *SeedBuilder {
	b.opUnblock()
	b.pickID(id)
	b.pickID(blockerID)

	return b
}

// Show appends bytes for a show operation.
func (b *SeedBuilder) Show(id string) *SeedBuilder {
	b.opShow()
	b.pickID(id)

	return b
}

// LSArgs configures the LS operation encoding.
type LSArgs struct {
	Status   string
	Priority int
	Type     string
}

// LS appends bytes for an ls operation with the provided filters.
func (b *SeedBuilder) LS(args LSArgs) *SeedBuilder {
	b.opLS()

	// Status filter.
	if args.Status == "" {
		b.appendBool(false)
	} else {
		idx := indexOf(seedStatuses, args.Status)
		if idx < 0 {
			panic(fmt.Sprintf("seed builder: unknown status %q", args.Status))
		}

		b.appendBool(true)
		b.inputValid()
		b.appendInt(idx)
	}

	// Priority filter.
	if args.Priority == 0 {
		b.appendBool(false)
	} else {
		if args.Priority < 1 || args.Priority > 4 {
			panic(fmt.Sprintf("seed builder: invalid LS priority %d", args.Priority))
		}

		b.appendBool(true)
		b.inputValid()
		b.appendInt(args.Priority - 1)
	}

	// Type filter.
	if args.Type == "" {
		b.appendBool(false)
	} else {
		idx := indexOf(seedTypes, args.Type)
		if idx < 0 {
			panic(fmt.Sprintf("seed builder: unknown type %q", args.Type))
		}

		b.appendBool(true)
		b.inputValid()
		b.appendInt(idx)
	}

	return b
}

// Ready appends bytes for a ready operation.
//
// limit uses tk semantics: 0 means "no limit".
func (b *SeedBuilder) Ready(limit int) *SeedBuilder {
	b.opReady()

	if limit <= 0 {
		// Encode "no limit" (no NextInt consumed).
		b.appendBool(false)

		return b
	}

	if limit > 9 {
		panic(fmt.Sprintf("seed builder: invalid ready limit %d", limit))
	}

	b.appendBool(true)
	b.appendInt(limit)

	return b
}

// nextID returns the next auto-generated ID (T0, T1, T2...).
func (b *SeedBuilder) nextID() string {
	id := fmt.Sprintf("T%d", b.nextIDNum)
	b.nextIDNum++
	b.knownIDs = append(b.knownIDs, id)

	return id
}

// -----------------------------------------------------------------------------
// Internals
// -----------------------------------------------------------------------------

var seedTitles = []string{
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

var seedTypes = []string{"bug", "feature", "task", "epic", "chore"}

var seedStatuses = []string{"open", "in_progress", "closed"}

var seedInvalidIDs = []string{"", "nonexistent", "INVALID-999"}

func (b *SeedBuilder) opChoice(start, rate int) {
	if rate <= 0 {
		panic(fmt.Sprintf("seed builder: op rate is zero at start=%d", start))
	}

	// NextOp uses choice := NextByte()%100. Any value in [start, start+rate)
	// selects this op. We always choose the range start for stability.
	b.appendByte(byte(start % 100))
}

func (b *SeedBuilder) opCreate() {
	b.opChoice(0, b.cfg.CreateRate)
}

func (b *SeedBuilder) opStart() {
	b.opChoice(b.cfg.CreateRate, b.cfg.StartRate)
}

func (b *SeedBuilder) opClose() {
	b.opChoice(b.cfg.CreateRate+b.cfg.StartRate, b.cfg.CloseRate)
}

func (b *SeedBuilder) opReopen() {
	b.opChoice(b.cfg.CreateRate+b.cfg.StartRate+b.cfg.CloseRate, b.cfg.ReopenRate)
}

func (b *SeedBuilder) opBlock() {
	start := b.cfg.CreateRate + b.cfg.StartRate + b.cfg.CloseRate + b.cfg.ReopenRate
	b.opChoice(start, b.cfg.BlockRate)
}

func (b *SeedBuilder) opUnblock() {
	start := b.cfg.CreateRate + b.cfg.StartRate + b.cfg.CloseRate + b.cfg.ReopenRate + b.cfg.BlockRate
	b.opChoice(start, b.cfg.UnblockRate)
}

func (b *SeedBuilder) opShow() {
	start := b.cfg.CreateRate + b.cfg.StartRate + b.cfg.CloseRate + b.cfg.ReopenRate + b.cfg.BlockRate + b.cfg.UnblockRate
	b.opChoice(start, b.cfg.ShowRate)
}

func (b *SeedBuilder) opLS() {
	start := b.cfg.CreateRate + b.cfg.StartRate + b.cfg.CloseRate + b.cfg.ReopenRate +
		b.cfg.BlockRate + b.cfg.UnblockRate + b.cfg.ShowRate
	b.opChoice(start, b.cfg.LSRate)
}

func (b *SeedBuilder) opReady() {
	// NextOp falls through to Ready when choice is not in earlier buckets.
	start := b.cfg.CreateRate + b.cfg.StartRate + b.cfg.CloseRate + b.cfg.ReopenRate +
		b.cfg.BlockRate + b.cfg.UnblockRate + b.cfg.ShowRate + b.cfg.LSRate

	if start >= 100 {
		panic("seed builder: Ready cannot be selected when non-ready rates sum to 100")
	}

	b.appendByte(byte(start))
}

func (b *SeedBuilder) inputValid() {
	rate := b.cfg.InvalidInputRate
	if rate >= 100 {
		panic("seed builder: cannot force valid input when InvalidInputRate=100")
	}

	// shouldBeInvalid is true when v%100 < rate. Setting v==rate yields false.
	b.appendByte(byte(rate))
}

func (b *SeedBuilder) inputInvalid() {
	rate := b.cfg.InvalidInputRate
	if rate <= 0 {
		panic("seed builder: cannot force invalid input when InvalidInputRate=0")
	}

	b.appendByte(0)
}

func (b *SeedBuilder) idValid() {
	rate := b.cfg.InvalidIDRate
	if rate >= 100 {
		panic("seed builder: cannot force valid ID when InvalidIDRate=100")
	}

	b.appendByte(byte(rate))
}

func (b *SeedBuilder) idInvalid() {
	rate := b.cfg.InvalidIDRate
	if rate <= 0 {
		panic("seed builder: cannot force invalid ID when InvalidIDRate=0")
	}

	b.appendByte(0)
}

func (b *SeedBuilder) pickID(id string) {
	idx := indexOf(b.knownIDs, id)
	if idx < 0 {
		panic(fmt.Sprintf("seed builder: unknown ID %q (known=%v)", id, b.knownIDs))
	}

	b.idValid()
	b.appendInt(idx)
}

func (b *SeedBuilder) pickInvalidID(id string) {
	idx := indexOf(seedInvalidIDs, id)
	if idx < 0 {
		panic(fmt.Sprintf("seed builder: unknown invalid ID %q", id))
	}

	b.idInvalid()
	b.appendInt(idx)
}

func (b *SeedBuilder) appendByte(v byte) {
	b.data = append(b.data, v)
}

func (b *SeedBuilder) appendBool(v bool) {
	if v {
		b.data = append(b.data, 1)

		return
	}

	b.data = append(b.data, 0)
}

func (b *SeedBuilder) appendInt(v int) {
	b.data = append(b.data, byte(v))
}

func indexOf(list []string, want string) int {
	for i := range list {
		if list[i] == want {
			return i
		}
	}

	return -1
}
