package cli_test

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"tk/internal/cli"
	"tk/internal/ticket"
)

// FuzzStateMachine tests that the CLI behaves like the model.
// It generates random sequences of operations and verifies both
// the model and real CLI produce the same results.
func FuzzStateMachine(f *testing.F) {
	// Boundary values for RNG seeding
	f.Add(int64(0))             // Zero - deterministic baseline
	f.Add(int64(1))             // Minimal positive
	f.Add(int64(-1))            // Negative seed
	f.Add(int64(math.MaxInt64)) // Maximum int64
	f.Add(int64(math.MinInt64)) // Minimum int64

	// Powers of 2 (often expose off-by-one errors in bit operations)
	f.Add(int64(1 << 16)) // 65536
	f.Add(int64(1 << 32)) // 4294967296 - 32-bit boundary
	f.Add(int64(1 << 62)) // Large power of 2 (within int64 range)

	// Arbitrary values for diversity
	f.Add(int64(12345))
	f.Add(int64(42))

	f.Fuzz(func(t *testing.T, seed int64) {
		rng := rand.New(rand.NewSource(seed))
		c := cli.NewCLI(t)
		model := NewModel()

		// Track ticket IDs for use in subsequent operations
		var realIDs []string

		var ops []string

		// Run 5-34 random operations per fuzz iteration.
		// At ~800 iterations/sec for 30s, this executes ~500k operations total.
		numOps := rng.Intn(30) + 5
		for range numOps {
			op := genOp(rng, model, realIDs)
			ops = append(ops, op.String())

			// Run CLI first - for CreateOp, this stores the new ID in the op struct
			createdID, realErr := op.Run(c)

			// Apply to model - for CreateOp, this uses the ID that Run() stored
			modelErr := op.Apply(model)

			// Both should succeed or both should fail
			modelOK := modelErr == nil
			realOK := realErr == nil

			if modelOK != realOK {
				t.Fatalf("error mismatch: model_err=%v real_err=%v\n%s",
					modelErr, realErr, formatOps(ops))
			}

			// Track created ticket IDs
			if createdID != "" && realOK {
				realIDs = append(realIDs, createdID)
			}

			// Verify state after each operation to catch divergence immediately
			err := verifyStateMatches(t, model, c.TicketDir(), ops)
			if err != nil {
				t.Fatal(err)
			}
		}

		// Check invariants
		checkInvariants(t, c.TicketDir())
	})
}

// FuzzCLI tests that the CLI never panics on arbitrary input.
func FuzzCLI(f *testing.F) {
	// === Empty/whitespace/help ===
	f.Add("")
	f.Add(" ")
	f.Add("\t")
	f.Add("--help")
	f.Add("-h")

	// === create command ===
	f.Add("create test")
	f.Add("create test -d description")
	f.Add("create test --type bug")
	f.Add("create test -p 1")
	f.Add("create")                // Missing title
	f.Add("create --type invalid") // Invalid type
	f.Add("create test -p 0")      // Invalid priority
	f.Add("create test -p abc")    // Non-numeric priority
	f.Add("create 日本語")            // Unicode title

	// === ls command ===
	f.Add("ls")
	f.Add("ls --status open")
	f.Add("ls --status closed")
	f.Add("ls --status in_progress")
	f.Add("ls --status invalid") // Invalid status
	f.Add("ls --invalid-flag")
	f.Add("ls -a") // Unknown short flag

	// === show command ===
	f.Add("show")          // Missing ID
	f.Add("show d5e1sd8")  // Valid format, nonexistent
	f.Add("show d5e1sd")   // Too short
	f.Add("show d5e1sd8a") // Too long
	f.Add("show 0000000")  // All zeros
	f.Add("show zzzzzzz")  // All z's
	f.Add("show ABCDEFG")  // Uppercase
	f.Add("show d5e1ld8")  // Invalid char 'l'

	// === start command ===
	f.Add("start")         // Missing ID
	f.Add("start d5e1sd8") // Valid format
	f.Add("start invalid") // Invalid ID
	f.Add("start abc def") // Extra args

	// === close command ===
	f.Add("close")         // Missing ID
	f.Add("close d5e1sd8") // Valid format
	f.Add("close invalid") // Invalid ID

	// === reopen command ===
	f.Add("reopen")         // Missing ID
	f.Add("reopen d5e1sd8") // Valid format
	f.Add("reopen invalid") // Invalid ID

	// === block command ===
	f.Add("block")                 // Missing both IDs
	f.Add("block d5e1sd8")         // Missing blocker
	f.Add("block d5e1sd8 d5e1sd9") // Both IDs
	f.Add("block d5e1sd8")         // Self-block attempt
	f.Add("block invalid")         // Invalid IDs

	// === unblock command ===
	f.Add("unblock")                 // Missing both
	f.Add("unblock d5e1sd8")         // Missing blocker
	f.Add("unblock d5e1sd8 d5e1sd9") // Both IDs

	// === ready command ===
	f.Add("ready")
	f.Add("ready --invalid")

	// === Unknown commands ===
	f.Add("unknown")
	f.Add("delete")
	f.Add("remove")

	// === Edge cases (apply to multiple commands) ===
	f.Add("show " + strings.Repeat("a", 100))   // Long ID
	f.Add("create " + strings.Repeat("x", 500)) // Long title

	f.Fuzz(func(t *testing.T, input string) {
		c := cli.NewCLI(t)
		args := strings.Fields(input)

		// Should never panic
		c.Run(args...)

		// Check invariants even after garbage input
		checkInvariants(t, c.TicketDir())
	})
}

// verifyStateMatches compares model state against filesystem state.
// This is the core property check: model and reality must agree.
// Returns an error on first mismatch, nil if all matches.
func verifyStateMatches(t *testing.T, model *Model, ticketDir string, ops []string) error {
	t.Helper()

	// Get all ticket files from filesystem (lexicographically sorted)
	files, err := filepath.Glob(filepath.Join(ticketDir, "*.md"))
	if err != nil {
		return fmt.Errorf("failed to glob tickets: %w", err)
	}

	sort.Strings(files)

	// Extract IDs from filenames
	var realIDs []string

	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".md")
		realIDs = append(realIDs, id)
	}

	// Get model IDs (sorted lexicographically for comparison)
	modelIDs := model.List()
	sort.Strings(modelIDs)

	// Check count matches
	if len(realIDs) != len(modelIDs) {
		return fmt.Errorf("ticket count mismatch: model=%d real=%d\nmodel=%v\nreal=%v\n%s",
			len(modelIDs), len(realIDs), modelIDs, realIDs, formatOps(ops))
	}

	// Check IDs match (lexicographic order)
	if !slices.Equal(realIDs, modelIDs) {
		return fmt.Errorf("ticket IDs mismatch:\nmodel=%v\nreal=%v\n%s",
			modelIDs, realIDs, formatOps(ops))
	}

	// Check each ticket's fields match
	for _, id := range modelIDs {
		modelTk, _ := model.Get(id)
		path := filepath.Join(ticketDir, id+".md")

		realTk, err := ticket.ParseTicketFrontmatter(path)
		if err != nil {
			return fmt.Errorf("failed to parse ticket %s: %w\n%s", id, err, formatOps(ops))
		}

		// Status
		if modelTk.Status != realTk.Status {
			return fmt.Errorf("status mismatch: want=%q got=%q\n%s",
				modelTk.Status, realTk.Status, formatOps(ops))
		}

		// Priority
		if modelTk.Priority != realTk.Priority {
			return fmt.Errorf("priority mismatch: want=%d got=%d\n%s",
				modelTk.Priority, realTk.Priority, formatOps(ops))
		}

		// Type
		if modelTk.Type != realTk.Type {
			return fmt.Errorf("type mismatch: want=%q got=%q\n%s",
				modelTk.Type, realTk.Type, formatOps(ops))
		}

		// BlockedBy (sort both for comparison)
		realBlocked := slices.Clone(realTk.BlockedBy)
		modelBlocked := slices.Clone(modelTk.BlockedBy)

		sort.Strings(realBlocked)
		sort.Strings(modelBlocked)

		if !slices.Equal(modelBlocked, realBlocked) {
			return fmt.Errorf("blocked-by mismatch: want=%v got=%v\n%s",
				modelTk.BlockedBy, realTk.BlockedBy, formatOps(ops))
		}

		// Closed timestamp presence
		hasClosed := realTk.Closed != ""
		if modelTk.HasClosed != hasClosed {
			return fmt.Errorf("closed timestamp mismatch: want=%v got=%v\n%s",
				modelTk.HasClosed, hasClosed, formatOps(ops))
		}

		// Title
		if modelTk.Title != realTk.Title {
			return fmt.Errorf("title mismatch: want=%q got=%q\n%s",
				modelTk.Title, realTk.Title, formatOps(ops))
		}

		// Assignee
		if modelTk.Assignee != realTk.Assignee {
			return fmt.Errorf("assignee mismatch: want=%q got=%q\n%s",
				modelTk.Assignee, realTk.Assignee, formatOps(ops))
		}
	}

	return nil
}

// formatOps formats the operation list for readability.
// The last operation is marked as causing the divergence.
func formatOps(ops []string) string {
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
			b.WriteString("  ← divergence\n")
		} else {
			b.WriteString("  ")
			b.WriteString(op)
		}
	}

	return b.String()
}

// checkInvariants verifies that the ticket directory is in a valid state.
// Uses t.Errorf to report all violations rather than stopping at the first.
func checkInvariants(t *testing.T, ticketDir string) {
	t.Helper()

	// Skip if ticket dir doesn't exist yet
	if _, err := os.Stat(ticketDir); os.IsNotExist(err) {
		return
	}

	files, err := filepath.Glob(filepath.Join(ticketDir, "*.md"))
	if err != nil {
		t.Errorf("failed to glob ticket files: %v", err)

		return
	}

	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("unreadable file %s: %v", f, err)

			continue
		}

		content := string(data)

		// Invariant: File is not empty
		if len(data) == 0 {
			t.Errorf("empty file: %s", f)

			continue
		}

		// Invariant: Has frontmatter delimiters
		if !strings.Contains(content, "---") {
			t.Errorf("missing frontmatter: %s", f)
		}

		// Invariant: Has status field
		if !strings.Contains(content, "status:") {
			t.Errorf("missing status field: %s", f)
		}

		// Invariant: Status is valid
		hasValidStatus := strings.Contains(content, "status: open") ||
			strings.Contains(content, "status: in_progress") ||
			strings.Contains(content, "status: closed")
		if !hasValidStatus {
			t.Errorf("invalid status in: %s", f)
		}

		// Invariant: Has ID field
		if !strings.Contains(content, "id:") {
			t.Errorf("missing id field: %s", f)
		}

		// Invariant: Has title (markdown header)
		if !strings.Contains(content, "# ") {
			t.Errorf("missing title: %s", f)
		}

		// Invariant: Closed tickets must have closed timestamp
		if strings.Contains(content, "status: closed") {
			if !strings.Contains(content, "closed:") {
				t.Errorf("closed ticket missing closed timestamp: %s", f)
			}
		}

		// Invariant: Non-closed tickets must NOT have closed timestamp
		if !strings.Contains(content, "status: closed") {
			if strings.Contains(content, "closed: 20") { // closed: 2025-... etc
				t.Errorf("non-closed ticket has closed timestamp: %s", f)
			}
		}

		// Invariant: Has required fields
		requiredFields := []string{"schema_version:", "type:", "priority:", "created:", "blocked-by:"}
		for _, field := range requiredFields {
			if !strings.Contains(content, field) {
				t.Errorf("missing required field %s in: %s", field, f)
			}
		}
	}

	// Invariant: Only .md files and .cache allowed in ticket dir
	entries, err := os.ReadDir(ticketDir)
	if err != nil {
		t.Errorf("failed to read ticket dir: %v", err)

		return
	}

	allowedFiles := map[string]bool{".cache": true, ".locks": true}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			if name != ".locks" {
				t.Errorf("unexpected directory in ticket dir: %s", name)
			}

			continue
		}

		if !strings.HasSuffix(name, ".md") && !allowedFiles[name] {
			t.Errorf("unexpected file in ticket dir: %s", name)
		}
	}

	// Invariant: No lock files left behind
	locksDir := filepath.Join(ticketDir, ".locks")
	if _, err := os.Stat(locksDir); err == nil {
		locks, _ := filepath.Glob(filepath.Join(locksDir, "*.lock"))
		if len(locks) > 0 {
			t.Errorf("lock files left behind: %v", locks)
		}
	}
}
