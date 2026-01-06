package spec

import (
	"fmt"
	"testing"
)

func TestBlockerCycleDetection(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(m *Model) // create tickets and existing blocks
		targetID    string         // ticket to add blocker to
		blockedByID string         // ticket to add as blocker
		wantErr     ErrCode        // expected error code, empty if should succeed
		wantCycle   []string       // expected cycle path if error
	}{
		{
			name: "no cycle - simple block",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
			},
			targetID:    "A",
			blockedByID: "B",
			wantErr:     "",
		},
		{
			name: "no cycle - chain",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
				createTicket(m, "C")
				mustBlock(m, "A", "B") // A blocked by B
				mustBlock(m, "B", "C") // B blocked by C
			},
			targetID:    "A",
			blockedByID: "C",
			wantErr:     "",
		},
		{
			name: "direct cycle - A blocked by B, B blocked by A",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
				mustBlock(m, "A", "B") // A blocked by B
			},
			targetID:    "B",
			blockedByID: "A",
			wantErr:     ErrBlockerCycle,
			wantCycle:   []string{"B", "A", "B"},
		},
		{
			name: "indirect cycle - A→B→C→A",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
				createTicket(m, "C")
				mustBlock(m, "A", "B") // A blocked by B
				mustBlock(m, "B", "C") // B blocked by C
			},
			targetID:    "C",
			blockedByID: "A",
			wantErr:     ErrBlockerCycle,
			wantCycle:   []string{"C", "A", "B", "C"},
		},
		{
			name: "longer cycle - A→B→C→D→A",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
				createTicket(m, "C")
				createTicket(m, "D")
				mustBlock(m, "A", "B")
				mustBlock(m, "B", "C")
				mustBlock(m, "C", "D")
			},
			targetID:    "D",
			blockedByID: "A",
			wantErr:     ErrBlockerCycle,
			wantCycle:   []string{"D", "A", "B", "C", "D"},
		},
		{
			name: "branching - no cycle through other branch",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
				createTicket(m, "C")
				createTicket(m, "D")
				mustBlock(m, "A", "B") // A blocked by B
				mustBlock(m, "A", "C") // A blocked by C
				mustBlock(m, "C", "D") // C blocked by D
			},
			targetID:    "D",
			blockedByID: "B",
			wantErr:     "",
		},
		{
			name: "branching - cycle through one branch",
			setup: func(m *Model) {
				createTicket(m, "A")
				createTicket(m, "B")
				createTicket(m, "C")
				createTicket(m, "D")
				mustBlock(m, "A", "B") // A blocked by B
				mustBlock(m, "A", "C") // A blocked by C
				mustBlock(m, "C", "D") // C blocked by D
			},
			targetID:    "D",
			blockedByID: "A",
			wantErr:     ErrBlockerCycle,
			wantCycle:   []string{"D", "A", "C", "D"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			tt.setup(m)

			err := m.Block(UserBlockInput{ID: tt.targetID, BlockerID: tt.blockedByID})

			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}

				return
			}

			if err == nil {
				t.Errorf("expected error %s, got nil", tt.wantErr)

				return
			}

			if err.Code != tt.wantErr {
				t.Errorf("expected error code %s, got %s", tt.wantErr, err.Code)

				return
			}

			if tt.wantCycle != nil {
				gotCycle := getContextValue(err, "cycle")

				wantCycleStr := joinCycle(tt.wantCycle)
				if gotCycle != wantCycleStr {
					t.Errorf("expected cycle %q, got %q", wantCycleStr, gotCycle)
				}
			}
		})
	}
}

var ticketCounter int

func createTicket(m *Model, id string) {
	ticketCounter++
	timestamp := fmt.Sprintf("2024-01-01T00:00:%02dZ", ticketCounter)

	_, err := m.Create(
		UserCreateInput{Title: "ticket " + id},
		FuzzCreateInput{ID: id, CreatedAt: timestamp},
	)
	if err != nil {
		panic("createTicket failed: " + err.Error())
	}
}

func mustBlock(m *Model, id, blockerID string) {
	err := m.Block(UserBlockInput{ID: id, BlockerID: blockerID})
	if err != nil {
		panic("mustBlock failed: " + err.Error())
	}
}

func getContextValue(err *Error, key string) string {
	for _, kv := range err.Context {
		if kv.K == key {
			return kv.V
		}
	}

	return ""
}

func joinCycle(path []string) string {
	if len(path) == 0 {
		return ""
	}

	result := path[0]
	for i := 1; i < len(path); i++ {
		result += "→" + path[i]
	}

	return result
}
