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
