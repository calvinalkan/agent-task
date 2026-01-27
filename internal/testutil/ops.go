// Package testutil provides ops and results for model-vs-CLI behavior tests.
package testutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/calvinalkan/agent-task/internal/testutil/spec"
)

// cliTicket is the JSON representation of a ticket from `ls --json`.
type cliTicket struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Priority  int      `json:"priority"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Parent    string   `json:"parent,omitempty"`
	BlockedBy []string `json:"blocked_by"`
	Created   string   `json:"created"`
	Closed    string   `json:"closed,omitempty"`
}

// Result is a generic operation result used by behavior tests.
//
// OK is true on success. Err is the model-side error (if any). Stdout/Stderr
// and ExitCode are populated for real CLI runs. Value is an optional
// canonicalized payload (IDs, tickets, etc.) for comparisons.
type Result struct {
	OK       bool
	Err      error
	ExitCode int
	Stdout   string
	Stderr   string
	Value    any
}

// ResultFromCLI creates a Result from a CLI invocation.
func ResultFromCLI(stdout, stderr string, exitCode int) Result {
	return Result{
		OK:       exitCode == 0,
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}
}

// ResultFromError creates a Result from a model error.
func ResultFromError(err error) Result {
	if err == nil {
		return Result{OK: true}
	}

	var specErr *spec.Error
	if errors.As(err, &specErr) && specErr == nil {
		return Result{OK: true}
	}

	return Result{
		OK:  false,
		Err: err,
	}
}

// Op is a behavior test operation executed against model and real CLI.
//
// ApplyReal should run the real CLI and capture stdout/stderr/exit code.
// ApplyModel should update the spec model using the same user/fuzz inputs.
type Op interface {
	ApplyModel(h *Harness) Result
	ApplyReal(h *Harness) Result
	String() string
}

// ErrorBucket groups broad error substrings for loose matching.
type ErrorBucket struct {
	Name       string
	Substrings []string
}

// Matches reports whether stderr contains any bucket substring (case-insensitive).
func (b ErrorBucket) Matches(stderr string) bool {
	if len(b.Substrings) == 0 {
		return false
	}

	lower := strings.ToLower(stderr)
	for _, s := range b.Substrings {
		if strings.Contains(lower, strings.ToLower(s)) {
			return true
		}
	}

	return false
}

// MatchAnyBucket reports whether stderr matches any of the buckets.
func MatchAnyBucket(stderr string, buckets []ErrorBucket) bool {
	for _, bucket := range buckets {
		if bucket.Matches(stderr) {
			return true
		}
	}

	return false
}

// OpCreate creates a new ticket.
type OpCreate struct {
	Title       string
	Description string
	Type        string
	Priority    int
	ParentID    string
	BlockedBy   []string
	CreatedID   string // Set after ApplyReal succeeds
}

// ApplyReal runs the create command against the real CLI.
func (o *OpCreate) ApplyReal(h *Harness) Result {
	args := []string{"create", o.Title}

	if o.Description != "" {
		args = append(args, "-d", o.Description)
	}

	if o.Type != "" {
		args = append(args, "-t", o.Type)
	}

	if o.Priority != 0 {
		args = append(args, "-p", strconv.Itoa(o.Priority))
	}

	if o.ParentID != "" {
		args = append(args, "--parent", o.ParentID)
	}

	for _, b := range o.BlockedBy {
		args = append(args, "--blocked-by", b)
	}

	stdout, stderr, code := h.CLI.Run(args...)
	res := ResultFromCLI(stdout, stderr, code)

	if res.OK {
		o.CreatedID = strings.TrimSpace(strings.Split(stdout, "\n")[0])
		res.Value = o.CreatedID
	}

	return res
}

// ApplyModel applies create to the spec model.
func (o *OpCreate) ApplyModel(h *Harness) Result {
	content := o.Title
	if o.Description != "" {
		content = o.Description
	}

	if content == "" {
		content = "placeholder"
	}

	user := &spec.UserCreateInput{
		Title:     o.Title,
		Content:   content,
		Type:      o.Type,
		Priority:  o.Priority,
		ParentID:  o.ParentID,
		BlockedBy: o.BlockedBy,
	}

	fuzzID := o.CreatedID
	if fuzzID == "" {
		fuzzID = h.Clock.NextTimestamp() + "_failed"
	}

	fuzz := spec.FuzzCreateInput{
		ID:        fuzzID,
		CreatedAt: h.Clock.NextTimestamp(),
	}

	id, err := h.Model.Create(user, fuzz)

	res := ResultFromError(err)
	if err == nil {
		res.Value = id
	}

	return res
}

func (o *OpCreate) String() string {
	return fmt.Sprintf("Create(%q)", o.Title)
}

// OpStart starts a ticket.
type OpStart struct {
	ID string
}

// ApplyReal runs the start command against the real CLI.
func (o OpStart) ApplyReal(h *Harness) Result {
	stdout, stderr, code := h.CLI.Run("start", o.ID)

	return ResultFromCLI(stdout, stderr, code)
}

// ApplyModel applies start to the spec model.
func (o OpStart) ApplyModel(h *Harness) Result {
	user := spec.UserStartInput{
		ID:        o.ID,
		ClaimedBy: claimedByForStart(o.ID),
	}

	fuzz := spec.FuzzStartInput{
		ClaimedAt: h.Clock.NextTimestamp(),
	}

	err := h.Model.Start(user, fuzz)

	return ResultFromError(err)
}

func (o OpStart) String() string {
	return fmt.Sprintf("Start(%s)", o.ID)
}

func claimedByForStart(id string) string {
	names := []string{"Alice", "Bob", "Charlie", "Diana"}

	sum := 0
	for i := range len(id) {
		sum += int(id[i])
	}

	return names[sum%len(names)]
}

// OpClose closes a ticket.
type OpClose struct {
	ID string
}

// ApplyReal runs the close command against the real CLI.
func (o OpClose) ApplyReal(h *Harness) Result {
	stdout, stderr, code := h.CLI.Run("close", o.ID)

	return ResultFromCLI(stdout, stderr, code)
}

// ApplyModel applies close to the spec model.
func (o OpClose) ApplyModel(h *Harness) Result {
	user := spec.UserCloseInput{ID: o.ID}
	fuzz := spec.FuzzCloseInput{ClosedAt: h.Clock.NextTimestamp()}

	err := h.Model.Close(user, fuzz)

	return ResultFromError(err)
}

func (o OpClose) String() string {
	return fmt.Sprintf("Close(%s)", o.ID)
}

// OpReopen reopens a ticket.
type OpReopen struct {
	ID string
}

// ApplyReal runs the reopen command against the real CLI.
func (o OpReopen) ApplyReal(h *Harness) Result {
	stdout, stderr, code := h.CLI.Run("reopen", o.ID)

	return ResultFromCLI(stdout, stderr, code)
}

// ApplyModel applies reopen to the spec model.
func (o OpReopen) ApplyModel(h *Harness) Result {
	err := h.Model.Reopen(spec.UserReopenInput{ID: o.ID})

	return ResultFromError(err)
}

func (o OpReopen) String() string {
	return fmt.Sprintf("Reopen(%s)", o.ID)
}

// OpBlock adds a blocker to a ticket.
type OpBlock struct {
	ID        string
	BlockerID string
}

// ApplyReal runs the block command against the real CLI.
func (o OpBlock) ApplyReal(h *Harness) Result {
	stdout, stderr, code := h.CLI.Run("block", o.ID, o.BlockerID)

	return ResultFromCLI(stdout, stderr, code)
}

// ApplyModel applies block to the spec model.
func (o OpBlock) ApplyModel(h *Harness) Result {
	err := h.Model.Block(spec.UserBlockInput{ID: o.ID, BlockerID: o.BlockerID})

	return ResultFromError(err)
}

func (o OpBlock) String() string {
	return fmt.Sprintf("Block(%s, %s)", o.ID, o.BlockerID)
}

// OpUnblock removes a blocker from a ticket.
type OpUnblock struct {
	ID        string
	BlockerID string
}

// ApplyReal runs the unblock command against the real CLI.
func (o OpUnblock) ApplyReal(h *Harness) Result {
	stdout, stderr, code := h.CLI.Run("unblock", o.ID, o.BlockerID)

	return ResultFromCLI(stdout, stderr, code)
}

// ApplyModel applies unblock to the spec model.
func (o OpUnblock) ApplyModel(h *Harness) Result {
	err := h.Model.Unblock(spec.UserUnblockInput{ID: o.ID, BlockerID: o.BlockerID})

	return ResultFromError(err)
}

func (o OpUnblock) String() string {
	return fmt.Sprintf("Unblock(%s, %s)", o.ID, o.BlockerID)
}

// OpShow shows a ticket.
type OpShow struct {
	ID string
}

// ApplyReal runs the show command against the real CLI.
func (o OpShow) ApplyReal(h *Harness) Result {
	stdout, stderr, code := h.CLI.Run("show", o.ID)

	return ResultFromCLI(stdout, stderr, code)
}

// ApplyModel applies show to the spec model.
func (o OpShow) ApplyModel(h *Harness) Result {
	tk, err := h.Model.Show(spec.UserShowInput{ID: o.ID})

	res := ResultFromError(err)
	if err == nil {
		res.Value = tk
	}

	return res
}

func (o OpShow) String() string {
	return fmt.Sprintf("Show(%s)", o.ID)
}

// OpLS lists tickets.
type OpLS struct {
	Status   string
	Priority int
	Type     string
	Limit    int
	Offset   int
}

// ApplyReal runs the ls command against the real CLI.
func (o OpLS) ApplyReal(h *Harness) Result {
	args := []string{"ls"}

	if o.Status != "" {
		args = append(args, "--status", o.Status)
	}

	if o.Priority != 0 {
		args = append(args, "--priority", strconv.Itoa(o.Priority))
	}

	if o.Type != "" {
		args = append(args, "--type", o.Type)
	}

	if o.Limit != 0 {
		args = append(args, "--limit", strconv.Itoa(o.Limit))
	}

	if o.Offset != 0 {
		args = append(args, "--offset", strconv.Itoa(o.Offset))
	}

	stdout, stderr, code := h.CLI.Run(args...)
	res := ResultFromCLI(stdout, stderr, code)

	if res.OK {
		res.Value = ParseLSOutput(stdout)
	}

	return res
}

// ApplyModel applies ls to the spec model.
func (o OpLS) ApplyModel(h *Harness) Result {
	user := spec.UserLSInput{
		Status:   o.Status,
		Priority: o.Priority,
		Type:     o.Type,
		Limit:    o.Limit,
		Offset:   o.Offset,
	}

	tickets, err := h.Model.LS(user)

	res := ResultFromError(err)
	if err == nil {
		ids := make([]string, len(tickets))
		for i := range tickets {
			ids[i] = tickets[i].ID
		}

		res.Value = ids
	}

	return res
}

func (o OpLS) String() string {
	parts := []string{"LS("}

	if o.Status != "" {
		parts = append(parts, "status="+o.Status)
	}

	if o.Priority != 0 {
		parts = append(parts, "priority="+strconv.Itoa(o.Priority))
	}

	if o.Type != "" {
		parts = append(parts, "type="+o.Type)
	}

	parts = append(parts, ")")

	return strings.Join(parts, "")
}

// OpReady lists ready tickets.
type OpReady struct {
	Limit int
}

// ApplyReal runs the ready command against the real CLI.
func (o OpReady) ApplyReal(h *Harness) Result {
	args := []string{"ready", "--field", "id"}

	if o.Limit != 0 {
		args = append(args, "--limit", strconv.Itoa(o.Limit))
	}

	stdout, stderr, code := h.CLI.Run(args...)
	res := ResultFromCLI(stdout, stderr, code)

	if res.OK {
		res.Value = ParseReadyOutput(stdout)
	}

	return res
}

// ApplyModel applies ready to the spec model.
func (o OpReady) ApplyModel(h *Harness) Result {
	tickets, err := h.Model.Ready(spec.UserReadyInput{Limit: o.Limit})

	res := ResultFromError(err)
	if err == nil {
		ids := make([]string, len(tickets))
		for i := range tickets {
			ids[i] = tickets[i].ID
		}

		res.Value = ids
	}

	return res
}

func (o OpReady) String() string {
	if o.Limit != 0 {
		return fmt.Sprintf("Ready(limit=%d)", o.Limit)
	}

	return "Ready()"
}

// ParseLSOutput extracts ticket IDs from ls output.
func ParseLSOutput(stdout string) []string {
	var ids []string

	for line := range strings.SplitSeq(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		if len(parts) > 0 && parts[0] != "" {
			ids = append(ids, parts[0])
		}
	}

	return ids
}

// ParseReadyOutput extracts ticket IDs from ready --field id output.
func ParseReadyOutput(stdout string) []string {
	var ids []string

	for line := range strings.SplitSeq(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		ids = append(ids, line)
	}

	return ids
}

// CompareState compares model state against CLI state using `ls --json`.
func CompareState(h *Harness, ops []string) error {
	modelIDs := h.Model.IDs()

	// Use CLI to get real state
	stdout, stderr, code := h.CLI.Run("ls", "--json", "--limit", "0")
	if code != 0 {
		return fmt.Errorf("ls --json failed: %s\n%s", stderr, FormatOps(ops))
	}

	var realTickets []cliTicket

	err := json.Unmarshal([]byte(stdout), &realTickets)
	if err != nil {
		return fmt.Errorf("failed to parse ls --json output: %w\n%s", err, FormatOps(ops))
	}

	// Build map for lookup
	realMap := make(map[string]cliTicket)

	var realIDs []string

	for i := range realTickets {
		tk := realTickets[i]
		realMap[tk.ID] = tk
		realIDs = append(realIDs, tk.ID)
	}

	if len(modelIDs) != len(realIDs) {
		return fmt.Errorf("ticket count mismatch: model=%d real=%d\nmodel=%v\nreal=%v\n%s",
			len(modelIDs), len(realIDs), modelIDs, realIDs, FormatOps(ops))
	}

	for _, id := range modelIDs {
		modelTk, _ := h.Model.Show(spec.UserShowInput{ID: id})
		realTk, ok := realMap[id]

		if !ok {
			return fmt.Errorf("ticket %s in model but not on disk\n%s", id, FormatOps(ops))
		}

		if string(modelTk.Status) != realTk.Status {
			return fmt.Errorf("status mismatch for %s: model=%q real=%q\n%s",
				id, modelTk.Status, realTk.Status, FormatOps(ops))
		}

		if int(modelTk.Priority) != realTk.Priority {
			return fmt.Errorf("priority mismatch for %s: model=%d real=%d\n%s",
				id, modelTk.Priority, realTk.Priority, FormatOps(ops))
		}

		if string(modelTk.Type) != realTk.Type {
			return fmt.Errorf("type mismatch for %s: model=%q real=%q\n%s",
				id, modelTk.Type, realTk.Type, FormatOps(ops))
		}

		if modelTk.Title != realTk.Title {
			return fmt.Errorf("title mismatch for %s: model=%q real=%q\n%s",
				id, modelTk.Title, realTk.Title, FormatOps(ops))
		}

		if modelTk.ParentID != realTk.Parent {
			return fmt.Errorf("parent mismatch for %s: model=%q real=%q\n%s",
				id, modelTk.ParentID, realTk.Parent, FormatOps(ops))
		}

		// Compare blocked_by lists
		modelBlockers := modelTk.BlockedBy
		if modelBlockers == nil {
			modelBlockers = []string{}
		}

		realBlockers := realTk.BlockedBy
		if realBlockers == nil {
			realBlockers = []string{}
		}

		if !slices.Equal(modelBlockers, realBlockers) {
			return fmt.Errorf("blocked_by mismatch for %s: model=%v real=%v\n%s",
				id, modelBlockers, realBlockers, FormatOps(ops))
		}

		modelHasClosed := !modelTk.ClosedAt.IsZero()
		realHasClosed := realTk.Closed != ""

		if modelHasClosed != realHasClosed {
			return fmt.Errorf("closed timestamp mismatch for %s: model=%v real=%v\n%s",
				id, modelHasClosed, realHasClosed, FormatOps(ops))
		}
	}

	return nil
}

// FormatOps formats the operation list for readability.
func FormatOps(ops []string) string {
	if len(ops) == 0 {
		return "Operations: (none)"
	}

	var b strings.Builder

	b.WriteString("Operations:")

	for i, op := range ops {
		b.WriteString("\n")

		if i == len(ops)-1 {
			b.WriteString("→ ")
			b.WriteString(op)
			b.WriteString("  ← divergence")
		} else {
			b.WriteString("  ")
			b.WriteString(op)
		}
	}

	return b.String()
}
