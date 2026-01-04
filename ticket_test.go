package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestNextSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", "a"},
		{"a", "b"},
		{"b", "c"},
		{"y", "z"},
		{"z", "za"},
		{"za", "zb"},
		{"zz", "zza"},
		{"zzz", "zzza"},
		{"zza", "zzb"},
		{"zzb", "zzc"},
		{"zzy", "zzz"},
	}

	for _, testCase := range tests {
		t.Run(testCase.input+"->"+testCase.want, func(t *testing.T) {
			t.Parallel()

			got := nextSuffix(testCase.input)
			if got != testCase.want {
				t.Errorf("nextSuffix(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestGenerateUniqueIDNoCollision(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("failed to create ticket dir: %v", err)
	}

	ticketID, err := GenerateUniqueID(ticketDir)
	if err != nil {
		t.Fatalf("GenerateUniqueID failed: %v", err)
	}

	// ID should be base ID with no suffix (7 chars from base32 encoding)
	if len(ticketID) != 7 {
		t.Errorf("expected base ID with 7 chars, got %q (len=%d)", ticketID, len(ticketID))
	}
}

func TestGenerateUniqueIDWithCollisions(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("failed to create ticket dir: %v", err)
	}

	// Generate first ID
	baseID, err := GenerateUniqueID(ticketDir)
	if err != nil {
		t.Fatalf("GenerateUniqueID failed: %v", err)
	}

	// Create a file with that ID to simulate collision
	err = os.WriteFile(filepath.Join(ticketDir, baseID+".md"), []byte("test"), 0o600)
	if err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// The next unique ID generated in the same second should have suffix
	// But since we can't control time, we manually create the collision scenario
	// by pre-creating files with predictable IDs

	// For now, just verify the function returns unique IDs
	id2, err := GenerateUniqueID(ticketDir)
	if err != nil {
		t.Fatalf("GenerateUniqueID failed on second call: %v", err)
	}

	if id2 == baseID {
		t.Errorf("second ID should be different from first, got same: %q", id2)
	}
}

func TestIDsSortLexicographically(t *testing.T) {
	t.Parallel()

	// Test that suffixed IDs sort correctly
	ids := []string{
		"d5czj08",
		"d5czj08a",
		"d5czj08b",
		"d5czj08z",
		"d5czj08za",
		"d5czj08zb",
		"d5czj08zz",
		"d5czj08zza",
	}

	// Shuffle and sort to verify
	shuffled := make([]string, len(ids))
	copy(shuffled, ids)
	shuffled[0], shuffled[3] = shuffled[3], shuffled[0]
	shuffled[1], shuffled[5] = shuffled[5], shuffled[1]

	slices.Sort(shuffled)

	for i, id := range shuffled {
		if id != ids[i] {
			t.Errorf("position %d: got %q, want %q", i, id, ids[i])
		}
	}
}

func TestConcurrentTicketCreation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	const numGoroutines = 5

	ticketIDs, failures := createTicketsConcurrently(t, tmpDir, numGoroutines)

	// All should succeed
	if len(failures) > 0 {
		for _, failure := range failures {
			t.Errorf("goroutine failed with exit code %d: stderr=%s", failure.code, failure.stderr)
		}
	}

	// All IDs should be unique
	if len(ticketIDs) != numGoroutines {
		t.Fatalf("expected %d IDs, got %d", numGoroutines, len(ticketIDs))
	}

	verifyUniqueIDs(t, ticketIDs)
	verifyTicketsRetrievable(t, tmpDir, ticketIDs)
	verifySortOrder(t, ticketIDs)
}

type createResult struct {
	stderr string
	code   int
}

func createTicketsConcurrently(t *testing.T, tmpDir string, count int) ([]string, []createResult) {
	t.Helper()

	var (
		waitGroup sync.WaitGroup
		mutex     sync.Mutex
		ticketIDs = make([]string, 0, count)
		failures  = make([]createResult, 0)
	)

	waitGroup.Add(count)

	for range count {
		go func() {
			defer waitGroup.Done()

			var stdout, stderr bytes.Buffer

			exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "create", "Concurrent ticket"}, nil)

			mutex.Lock()
			defer mutex.Unlock()

			if exitCode == 0 {
				ticketIDs = append(ticketIDs, strings.TrimSpace(stdout.String()))
			} else {
				failures = append(failures, createResult{stderr.String(), exitCode})
			}
		}()
	}

	waitGroup.Wait()

	return ticketIDs, failures
}

func verifyUniqueIDs(t *testing.T, ticketIDs []string) {
	t.Helper()

	seen := make(map[string]bool)

	for _, ticketID := range ticketIDs {
		if seen[ticketID] {
			t.Errorf("duplicate ID: %s", ticketID)
		}

		seen[ticketID] = true
	}
}

func verifyTicketsRetrievable(t *testing.T, tmpDir string, ticketIDs []string) {
	t.Helper()

	for _, ticketID := range ticketIDs {
		var stdout, stderr bytes.Buffer

		exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "show", ticketID}, nil)
		if exitCode != 0 {
			t.Errorf("tk show %s failed: %s", ticketID, stderr.String())
		}
	}
}

func verifySortOrder(t *testing.T, ticketIDs []string) {
	t.Helper()

	sorted := make([]string, len(ticketIDs))
	copy(sorted, ticketIDs)
	slices.Sort(sorted)

	// Verify sorting: shorter IDs before longer ones with same prefix
	for idx := 1; idx < len(sorted); idx++ {
		if sorted[idx] <= sorted[idx-1] {
			t.Errorf("IDs not properly sorted: %v", sorted)

			break
		}
	}
}

func TestSuffixedIDsRetrievable(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	// Create tickets rapidly to trigger suffix generation
	const numTickets = 5

	ticketIDs := make([]string, 0, numTickets)

	for range numTickets {
		var stdout bytes.Buffer

		exitCode := Run(nil, &stdout, nil, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
		if exitCode != 0 {
			t.Fatal("failed to create ticket")
		}

		ticketIDs = append(ticketIDs, strings.TrimSpace(stdout.String()))
	}

	// Verify all tickets are retrievable
	for _, ticketID := range ticketIDs {
		if !TicketExists(ticketDir, ticketID) {
			t.Errorf("ticket %s should exist", ticketID)
		}

		var stdout, stderr bytes.Buffer

		exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "show", ticketID}, nil)
		if exitCode != 0 {
			t.Errorf("tk show %s failed: %s", ticketID, stderr.String())
		}
	}
}
