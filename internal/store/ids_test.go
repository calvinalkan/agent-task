package store_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/calvinalkan/agent-task/internal/store"
)

// Contract: short IDs must ignore the timestamp to stay stable across clock drift.
func Test_ShortID_DoesNotChange_When_TimestampChanges(t *testing.T) {
	t.Parallel()

	randA := uint16(0xabc)
	randB := uint64(0x123456789abcde)

	idA := makeUUIDv7(t, time.Date(2026, 1, 27, 15, 23, 10, 0, time.UTC), randA, randB)
	idB := makeUUIDv7(t, time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC), randA, randB)
	idC := makeUUIDv7(t, time.Date(2026, 1, 27, 15, 23, 10, 0, time.UTC), randA, randB^(1<<61))

	shortA, err := store.ShortIDFromUUID(idA)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	shortB, err := store.ShortIDFromUUID(idB)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	shortC, err := store.ShortIDFromUUID(idC)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	if shortA != shortB {
		t.Fatalf("short id should ignore timestamp changes: %q vs %q", shortA, shortB)
	}

	if shortA == shortC {
		t.Fatal("short id should change when random bits change")
	}

	if len(shortA) != 12 {
		t.Fatalf("short id length mismatch: %d", len(shortA))
	}

	if !isCrockfordBase32(shortA) {
		t.Fatalf("short id contains non-Crockford chars: %q", shortA)
	}
}

// Contract: paths must use UTC timestamps to avoid local-time ambiguity.
func Test_TicketPath_ReturnsCanonicalPath_When_UUIDv7Provided(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 1, 27, 15, 23, 10, 0, time.UTC)
	id := makeUUIDv7(t, ts, 0xbee, 0x123456789abcde)

	shortID, err := store.ShortIDFromUUID(id)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	got, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	expectedDir := ts.UTC().Format("2006/01-02")

	want := filepath.Join(".tickets", expectedDir, shortID+".md")
	if got != want {
		t.Fatalf("ticket path mismatch\n got: %q\nwant: %q", got, want)
	}
}

// Contract: UUIDv7-only helpers should reject other versions early.
func Test_ShortID_ReturnsError_When_UUIDNotV7(t *testing.T) {
	t.Parallel()

	id := uuid.New()

	_, err := store.ShortIDFromUUID(id)
	if err == nil {
		t.Fatal("expected error for non-v7 uuid")
	}

	_, err = store.TicketPath(id)
	if err == nil {
		t.Fatal("expected error for non-v7 uuid")
	}
}

func makeUUIDv7(t *testing.T, ts time.Time, randA uint16, randB uint64) uuid.UUID {
	t.Helper()

	ms := uint64(ts.UnixMilli())
	if ms>>48 != 0 {
		t.Fatal("timestamp out of range for uuidv7")
	}

	var b [16]byte

	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	b[6] = byte(0x70 | ((randA >> 8) & 0x0f))
	b[7] = byte(randA)

	b[8] = byte(0x80 | ((randB >> 56) & 0x3f))
	b[9] = byte(randB >> 48)
	b[10] = byte(randB >> 40)
	b[11] = byte(randB >> 32)
	b[12] = byte(randB >> 24)
	b[13] = byte(randB >> 16)
	b[14] = byte(randB >> 8)
	b[15] = byte(randB)

	id := uuid.UUID(b)
	if id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		t.Fatal("constructed uuid is not v7")
	}

	return id
}

func isCrockfordBase32(value string) bool {
	for _, r := range value {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
			return false
		}
	}

	return true
}
