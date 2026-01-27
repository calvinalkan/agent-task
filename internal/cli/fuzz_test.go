package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
	"github.com/calvinalkan/agent-task/internal/testutil"
)

// FuzzCLI_Matches_Model_When_Random_Ops_Applied tests that the CLI behaves like the model.
// It derives operations from fuzz bytes and compares model vs real behavior.
func FuzzCLI_Matches_Model_When_Random_Ops_Applied(f *testing.F) {
	for _, seed := range testutil.CuratedSeeds() {
		f.Add(seed.Data)
	}

	f.Add([]byte{0x00, 0x01, 0x02})
	f.Add([]byte("tk-ops"))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		cfg := testutil.DefaultRunConfig()
		cfg.MaxOps = 150

		cfg.CompareStateEveryN = 10
		if testing.Short() {
			cfg.MaxOps = 50
			cfg.CompareStateEveryN = 5
		}

		testutil.RunBehaviorWithSeed(t, fuzzBytes, cfg)
	})
}

// FuzzCLI_DoesNotPanic_When_Invoked_With_Arbitrary_Input tests that the CLI never panics on arbitrary input.
func FuzzCLI_DoesNotCrash_Or_Leave_Invalid_State_When_Invoked_With_Arbitrary_Input(f *testing.F) {
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
	f.Add("create")                    // Missing title
	f.Add("create --type invalid")     // Invalid type
	f.Add("create test -p 0")          // Invalid priority
	f.Add("create test -p abc")        // Non-numeric priority
	f.Add("create \u65e5\u672c\u8a9e") // Unicode title (日本語)

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

// checkInvariants verifies that the ticket directory is in a valid state.
// Uses t.Errorf to report all violations rather than stopping at the first.
func checkInvariants(t *testing.T, ticketDir string) {
	t.Helper()

	// Skip if ticket dir doesn't exist yet
	_, statErr := os.Stat(ticketDir)
	if os.IsNotExist(statErr) {
		return
	}

	files, err := filepath.Glob(filepath.Join(ticketDir, "*.md"))
	if err != nil {
		t.Errorf("failed to glob ticket files: %v", err)

		return
	}

	for _, f := range files {
		var data []byte

		data, err = os.ReadFile(f)
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

	_, locksDirErr := os.Stat(locksDir)
	if locksDirErr == nil {
		locks, _ := filepath.Glob(filepath.Join(locksDir, "*.lock"))
		if len(locks) > 0 {
			t.Errorf("lock files left behind: %v", locks)
		}
	}
}
