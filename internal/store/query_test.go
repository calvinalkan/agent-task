package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/store"
)

// Contract: Open rebuilds when the index schema is missing so queries never hit a stale or empty DB.
func Test_Open_RebuildsIndex_When_UserVersionMissing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	id := makeUUIDv7(t, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC), 0xabc, 0x1111111111111111)

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC),
		Title:     "Bootstrap",
	})

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

	base := time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	idA := makeUUIDv7(t, base, 0xabc, 0x1111111111111111)
	idB := makeUUIDv7(t, base.Add(time.Hour), 0xabc, 0x2222222222222222)
	idC := makeUUIDv7(t, base.Add(2*time.Hour), 0xabc, 0x3333333333333333)

	closedAt := base.Add(2 * time.Hour)

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idA.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: base,
		BlockedBy: []string{idB.String(), idC.String()},
		Title:     "Alpha",
	})

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idB.String(),
		Status:    "closed",
		Type:      "bug",
		Priority:  2,
		CreatedAt: base.Add(time.Hour),
		ClosedAt:  &closedAt,
		Parent:    idA.String(),
		Title:     "Beta",
	})

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idC.String(),
		Status:    "in_progress",
		Type:      "feature",
		Priority:  3,
		CreatedAt: base.Add(2 * time.Hour),
		Assignee:  "sam",
		BlockedBy: []string{idA.String()},
		Title:     "Gamma",
	})

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	all, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query all: %v", err)
	}

	wantAll := []string{idA.String(), idB.String(), idC.String()}
	if len(all) != len(wantAll) {
		t.Fatalf("all count = %d, want %d", len(all), len(wantAll))
	}

	for i, want := range wantAll {
		if all[i].ID != want {
			t.Fatalf("all[%d].ID = %s, want %s", i, all[i].ID, want)
		}
	}

	if got := all[0].BlockedBy; len(got) != 2 || got[0] != idB.String() || got[1] != idC.String() {
		t.Fatalf("blocked_by for %s = %v, want [%s %s]", idA.String(), got, idB.String(), idC.String())
	}

	if all[1].ClosedAt == nil || !all[1].ClosedAt.Equal(closedAt.UTC()) {
		t.Fatalf("closed_at for %s = %v, want %v", idB.String(), all[1].ClosedAt, closedAt.UTC())
	}

	shortID, err := store.ShortIDFromUUID(idC)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	cases := []struct {
		name string
		opts store.QueryOptions
		want []string
	}{
		{
			name: "status",
			opts: store.QueryOptions{Status: "open"},
			want: []string{idA.String()},
		},
		{
			name: "type",
			opts: store.QueryOptions{Type: "feature"},
			want: []string{idC.String()},
		},
		{
			name: "priority",
			opts: store.QueryOptions{Priority: 2},
			want: []string{idB.String()},
		},
		{
			name: "parent",
			opts: store.QueryOptions{Parent: idA.String()},
			want: []string{idB.String()},
		},
		{
			name: "short_id",
			opts: store.QueryOptions{ShortIDPrefix: shortID},
			want: []string{idC.String()},
		},
		{
			name: "limit_offset",
			opts: store.QueryOptions{Limit: 1, Offset: 1},
			want: []string{idB.String()},
		},
		{
			name: "offset_only",
			opts: store.QueryOptions{Offset: 2},
			want: []string{idC.String()},
		},
		{
			name: "limit_only",
			opts: store.QueryOptions{Limit: 2},
			want: []string{idA.String(), idB.String()},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			results, err := storeHandle.Query(t.Context(), &tc.opts)
			if err != nil {
				t.Fatalf("query %s: %v", tc.name, err)
			}

			if len(results) != len(tc.want) {
				t.Fatalf("%s count = %d, want %d", tc.name, len(results), len(tc.want))
			}

			for i, want := range tc.want {
				if results[i].ID != want {
					t.Fatalf("%s[%d].ID = %s, want %s", tc.name, i, results[i].ID, want)
				}
			}
		})
	}
}

// Contract: Query with limit returns correct ticket count even when tickets have multiple blockers.
// This tests that LIMIT applies to tickets, not to joined rows.
func Test_Query_Limit_Counts_Tickets_Not_Rows_When_Blockers_Exist(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	base := time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)
	idA := makeUUIDv7(t, base, 0xabc, 0x1111111111111111)
	idB := makeUUIDv7(t, base.Add(time.Hour), 0xabc, 0x2222222222222222)
	idC := makeUUIDv7(t, base.Add(2*time.Hour), 0xabc, 0x3333333333333333)
	idD := makeUUIDv7(t, base.Add(3*time.Hour), 0xabc, 0x4444444444444444)

	// Ticket A has 3 blockers - without subquery, LIMIT 2 would return only A
	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idA.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: base,
		BlockedBy: []string{idB.String(), idC.String(), idD.String()},
		Title:     "Alpha",
	})

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idB.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: base.Add(time.Hour),
		Title:     "Beta",
	})

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idC.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: base.Add(2 * time.Hour),
		BlockedBy: []string{idA.String()},
		Title:     "Gamma",
	})

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idD.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: base.Add(3 * time.Hour),
		Title:     "Delta",
	})

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	// LIMIT 2 should return 2 tickets, not be confused by A's 3 blocker rows
	results, err := storeHandle.Query(t.Context(), &store.QueryOptions{Limit: 2})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("count = %d, want 2", len(results))
	}

	if results[0].ID != idA.String() {
		t.Fatalf("results[0].ID = %s, want %s", results[0].ID, idA.String())
	}

	if results[1].ID != idB.String() {
		t.Fatalf("results[1].ID = %s, want %s", results[1].ID, idB.String())
	}

	// Verify blockers are correctly attached
	if len(results[0].BlockedBy) != 3 {
		t.Fatalf("results[0].BlockedBy = %v, want 3 blockers", results[0].BlockedBy)
	}

	if len(results[1].BlockedBy) != 0 {
		t.Fatalf("results[1].BlockedBy = %v, want empty", results[1].BlockedBy)
	}

	// LIMIT 2 OFFSET 1 should return B and C
	results, err = storeHandle.Query(t.Context(), &store.QueryOptions{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("query with offset: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("offset count = %d, want 2", len(results))
	}

	if results[0].ID != idB.String() {
		t.Fatalf("offset results[0].ID = %s, want %s", results[0].ID, idB.String())
	}

	if results[1].ID != idC.String() {
		t.Fatalf("offset results[1].ID = %s, want %s", results[1].ID, idC.String())
	}

	// C has 1 blocker
	if len(results[1].BlockedBy) != 1 || results[1].BlockedBy[0] != idA.String() {
		t.Fatalf("offset results[1].BlockedBy = %v, want [%s]", results[1].BlockedBy, idA.String())
	}
}
