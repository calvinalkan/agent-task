package cli_test

import (
	"slices"
	"strings"
	"sync"
	"testing"

	"tk/internal/cli"
	"tk/internal/ticket"
)

func TestConcurrentTicketCreation(t *testing.T) {
	t.Parallel()

	h := cli.NewCLI(t)

	const numGoroutines = 5

	ticketIDs, failures := createTicketsConcurrently(t, h, numGoroutines)

	if len(failures) > 0 {
		for _, failure := range failures {
			t.Errorf("goroutine failed with exit code %d: stderr=%s", failure.code, failure.stderr)
		}
	}

	if got, want := len(ticketIDs), numGoroutines; got != want {
		t.Fatalf("ticketCount=%d, want=%d", got, want)
	}

	verifyUniqueIDs(t, ticketIDs)
	verifyTicketsRetrievable(t, h, ticketIDs)
	verifySortOrder(t, ticketIDs)
}

type createResult struct {
	stderr string
	code   int
}

func createTicketsConcurrently(t *testing.T, h *cli.CLI, count int) ([]string, []createResult) {
	t.Helper()

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		ticketIDs = make([]string, 0, count)
		failures  = make([]createResult, 0)
	)

	wg.Add(count)

	for range count {
		go func() {
			defer wg.Done()

			stdout, stderr, exitCode := h.Run("create", "Concurrent ticket")

			mu.Lock()
			defer mu.Unlock()

			if exitCode == 0 {
				ticketIDs = append(ticketIDs, strings.TrimSpace(stdout))
			} else {
				failures = append(failures, createResult{stderr, exitCode})
			}
		}()
	}

	wg.Wait()

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

func verifyTicketsRetrievable(t *testing.T, h *cli.CLI, ticketIDs []string) {
	t.Helper()

	for _, ticketID := range ticketIDs {
		_, stderr, exitCode := h.Run("show", ticketID)
		if got, want := exitCode, 0; got != want {
			t.Errorf("show %s: exitCode=%d, want=%d, stderr=%s", ticketID, got, want, stderr)
		}
	}
}

func verifySortOrder(t *testing.T, ticketIDs []string) {
	t.Helper()

	sorted := make([]string, len(ticketIDs))
	copy(sorted, ticketIDs)
	slices.Sort(sorted)

	for idx := 1; idx < len(sorted); idx++ {
		if sorted[idx] <= sorted[idx-1] {
			t.Errorf("IDs not properly sorted: %v", sorted)

			break
		}
	}
}

func TestSuffixedIDsRetrievable(t *testing.T) {
	t.Parallel()

	h := cli.NewCLI(t)

	const numTickets = 5

	ticketIDs := make([]string, 0, numTickets)
	for range numTickets {
		id := h.MustRun("create", "Test ticket")
		ticketIDs = append(ticketIDs, id)
	}

	for _, ticketID := range ticketIDs {
		if !ticket.Exists(h.TicketDir(), ticketID) {
			t.Errorf("ticket %s should exist", ticketID)
		}

		_, stderr, exitCode := h.Run("show", ticketID)
		if got, want := exitCode, 0; got != want {
			t.Errorf("show %s: exitCode=%d, want=%d, stderr=%s", ticketID, got, want, stderr)
		}
	}
}
