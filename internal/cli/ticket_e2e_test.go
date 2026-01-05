package cli

import (
	"bytes"
	"slices"
	"strings"
	"sync"
	"testing"

	"tk/internal/ticket"
)

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
	ticketDir := tmpDir + "/.tickets"

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
		if !ticket.Exists(ticketDir, ticketID) {
			t.Errorf("ticket %s should exist", ticketID)
		}

		var stdout, stderr bytes.Buffer

		exitCode := Run(nil, &stdout, &stderr, []string{"tk", "-C", tmpDir, "show", ticketID}, nil)
		if exitCode != 0 {
			t.Errorf("tk show %s failed: %s", ticketID, stderr.String())
		}
	}
}
