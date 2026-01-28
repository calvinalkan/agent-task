package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/store"
	"github.com/google/uuid"
)

// Contract: Open rebuilds when the index schema is missing so queries never hit a stale or empty DB.
func Test_Open_RebuildsIndex_When_UserVersionMissing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	ticket := newTestTicket(t, "Bootstrap")
	writeTicketFile(t, ticketDir, ticket)

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	db := openIndex(t, ticketDir)
	t.Cleanup(func() { _ = db.Close() })

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	const wantSchemaVersion = 1
	if version != wantSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, wantSchemaVersion)
	}

	if count := countTickets(t, db); count != 1 {
		t.Fatalf("ticket count = %d, want 1", count)
	}
}

// Contract: Query applies SQLite filters and preserves ID ordering for stable CLI output.
func Test_Query_Applies_Filters_And_Ordering_When_Options_Are_Set(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	// Create tickets with different attributes
	ticketA, _ := store.NewTicket("Alpha", "task", "open", 1)
	ticketB, _ := store.NewTicket("Beta", "bug", "closed", 2)
	ticketB.ClosedAt = store.TimePtr(time.Now().UTC())
	ticketC, _ := store.NewTicket("Gamma", "feature", "in_progress", 3)
	ticketC.Assignee = store.StringPtr("sam")

	// Set up relationships
	ticketA.BlockedBy = uuid.UUIDs{ticketB.ID, ticketC.ID}
	ticketB.Parent = &ticketA.ID
	ticketC.BlockedBy = uuid.UUIDs{ticketA.ID}

	ticketA = putTicket(t.Context(), t, storeHandle, ticketA)
	putTicket(t.Context(), t, storeHandle, ticketB)
	putTicket(t.Context(), t, storeHandle, ticketC)

	_, err = storeHandle.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	all, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query all: %v", err)
	}

	if len(all) != 3 {
		t.Fatalf("all count = %d, want 3", len(all))
	}

	// Verify blockers are correctly attached
	for _, ticket := range all {
		if ticket.ID == ticketA.ID && len(ticket.BlockedBy) != 2 {
			t.Fatalf("blocked_by for %s = %v, want 2 blockers", ticketA.ID, ticket.BlockedBy)
		}
	}

	cases := []struct {
		name  string
		opts  store.QueryOptions
		count int
	}{
		{name: "status_open", opts: store.QueryOptions{Status: "open"}, count: 1},
		{name: "status_closed", opts: store.QueryOptions{Status: "closed"}, count: 1},
		{name: "type_feature", opts: store.QueryOptions{Type: "feature"}, count: 1},
		{name: "priority_2", opts: store.QueryOptions{Priority: 2}, count: 1},
		{name: "parent", opts: store.QueryOptions{Parent: ticketA.ID.String()}, count: 1},
		{name: "limit_1", opts: store.QueryOptions{Limit: 1}, count: 1},
		{name: "limit_2", opts: store.QueryOptions{Limit: 2}, count: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			results, err := storeHandle.Query(t.Context(), &tc.opts)
			if err != nil {
				t.Fatalf("query %s: %v", tc.name, err)
			}

			if len(results) != tc.count {
				t.Fatalf("%s count = %d, want %d", tc.name, len(results), tc.count)
			}
		})
	}
}

// Contract: Query with limit returns correct ticket count even when tickets have multiple blockers.
func Test_Query_Limit_Counts_Tickets_Not_Rows_When_Blockers_Exist(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	ticketA, _ := store.NewTicket("Alpha", "task", "open", 1)
	ticketB, _ := store.NewTicket("Beta", "task", "open", 1)
	ticketC, _ := store.NewTicket("Gamma", "task", "open", 1)
	ticketD, _ := store.NewTicket("Delta", "task", "open", 1)

	// Ticket A has 3 blockers
	ticketA.BlockedBy = uuid.UUIDs{ticketB.ID, ticketC.ID, ticketD.ID}
	ticketC.BlockedBy = uuid.UUIDs{ticketA.ID}

	putTicket(t.Context(), t, storeHandle, ticketA)
	putTicket(t.Context(), t, storeHandle, ticketB)
	putTicket(t.Context(), t, storeHandle, ticketC)
	putTicket(t.Context(), t, storeHandle, ticketD)

	_, err = storeHandle.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// LIMIT 2 should return 2 tickets, not be confused by A's 3 blocker rows
	results, err := storeHandle.Query(t.Context(), &store.QueryOptions{Limit: 2})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("count = %d, want 2", len(results))
	}
}

// Contract: GetByPrefix returns single ticket when prefix uniquely matches short_id.
func Test_GetByPrefix_Returns_Single_Ticket_When_ShortID_Matches(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket := putTicket(t.Context(), t, s, newTestTicket(t, "Test Ticket"))

	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// Use first 4 chars of short_id as prefix
	prefix := ticket.ShortID[:4]

	tickets, err := s.GetByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}

	if tickets[0].ID != ticket.ID {
		t.Fatalf("id = %s, want %s", tickets[0].ID, ticket.ID)
	}

	if tickets[0].ShortID != ticket.ShortID {
		t.Fatalf("short_id = %s, want %s", tickets[0].ShortID, ticket.ShortID)
	}
}

// Contract: GetByPrefix returns single ticket when prefix matches full UUID.
func Test_GetByPrefix_Returns_Single_Ticket_When_UUID_Prefix_Matches(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket := putTicket(t.Context(), t, s, newTestTicket(t, "Test Ticket"))

	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// Use first 8 chars of UUID as prefix
	prefix := ticket.ID.String()[:8]

	tickets, err := s.GetByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}

	if tickets[0].ID != ticket.ID {
		t.Fatalf("id = %s, want %s", tickets[0].ID, ticket.ID)
	}
}

// Contract: GetByPrefix returns empty slice when no tickets match the prefix.
func Test_GetByPrefix_Returns_Empty_When_No_Match(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	putTicket(t.Context(), t, s, newTestTicket(t, "Test Ticket"))

	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	tickets, err := s.GetByPrefix(t.Context(), "ZZZZZZ")
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(tickets) != 0 {
		t.Fatalf("tickets = %d, want 0", len(tickets))
	}
}

// Contract: GetByPrefix returns multiple tickets when prefix is ambiguous.
func Test_GetByPrefix_Returns_Multiple_Tickets_When_Prefix_Ambiguous(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket1 := putTicket(t.Context(), t, s, newTestTicket(t, "Ticket One"))
	ticket2 := putTicket(t.Context(), t, s, newTestTicket(t, "Ticket Two"))

	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// Both UUIDs share the same timestamp prefix (first 8 chars of UUIDv7 are timestamp-based)
	// So using a short prefix should match both
	prefix := ticket1.ID.String()[:8]

	tickets, err := s.GetByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(tickets) != 2 {
		t.Fatalf("tickets = %d, want 2", len(tickets))
	}

	// Verify both tickets are returned
	ids := map[uuid.UUID]bool{tickets[0].ID: true, tickets[1].ID: true}
	if !ids[ticket1.ID] || !ids[ticket2.ID] {
		t.Fatalf("expected both tickets, got %v", ids)
	}
}

// Contract: GetByPrefix returns error when prefix is empty.
func Test_GetByPrefix_Returns_Error_When_Prefix_Empty(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	_, err = s.GetByPrefix(t.Context(), "")
	if err == nil {
		t.Fatal("expected error for empty prefix")
	}
}

// Contract: GetByPrefix includes blockers in returned tickets.
func Test_GetByPrefix_Includes_Blockers_When_Present(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	blocker := putTicket(t.Context(), t, s, newTestTicket(t, "Blocker"))

	blocked, _ := store.NewTicket("Blocked Ticket", "task", "open", 2)
	blocked.BlockedBy = uuid.UUIDs{blocker.ID}
	blocked = putTicket(t.Context(), t, s, blocked)

	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	tickets, err := s.GetByPrefix(t.Context(), blocked.ShortID[:4])
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}

	if len(tickets[0].BlockedBy) != 1 {
		t.Fatalf("blockers = %d, want 1", len(tickets[0].BlockedBy))
	}

	if tickets[0].BlockedBy[0] != blocker.ID {
		t.Fatalf("blocker = %s, want %s", tickets[0].BlockedBy[0], blocker.ID)
	}
}
