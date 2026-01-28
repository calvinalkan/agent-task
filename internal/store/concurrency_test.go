package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/store"
	"github.com/calvinalkan/agent-task/pkg/fs"
)

const testLockTimeout = 10 * time.Millisecond

// Contract: Get uses a shared lock, so it succeeds when another shared lock is held.
func Test_Get_Succeeds_When_Shared_Lock_Held(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket, err := store.NewTicket("Test Ticket", "task", "open", 2)
	if err != nil {
		t.Fatalf("new ticket: %v", err)
	}

	putTicket(t.Context(), t, s, ticket)

	// Hold a shared lock - Get should still succeed (shared + shared = ok)
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	lock, err := locker.RLock(walPath)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	got, err := s.Get(t.Context(), ticket.ID.String())
	if err != nil {
		t.Fatalf("get while shared lock held: %v", err)
	}

	if got.ID != ticket.ID {
		t.Fatalf("id = %s, want %s", got.ID, ticket.ID)
	}
}

// Contract: Get blocks when an exclusive lock is held, timing out after lockTimeout.
func Test_Get_Returns_Error_When_Exclusive_Lock_Held(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket, err := store.NewTicket("Test Ticket", "task", "open", 2)
	if err != nil {
		t.Fatalf("new ticket: %v", err)
	}

	putTicket(t.Context(), t, s, ticket)

	// Hold an exclusive lock - Get should block and timeout
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	lock, err := locker.Lock(walPath)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	_, err = s.Get(t.Context(), ticket.ID.String())
	if err == nil {
		t.Fatal("expected error when exclusive lock held")
	}

	if !isDeadlineExceeded(err) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

// Contract: Query uses a shared lock, so it succeeds when another shared lock is held.
func Test_Query_Succeeds_When_Shared_Lock_Held(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket, err := store.NewTicket("Test Ticket", "task", "open", 2)
	if err != nil {
		t.Fatalf("new ticket: %v", err)
	}

	putTicket(t.Context(), t, s, ticket)

	// Reindex to populate SQLite
	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// Hold a shared lock - Query should still succeed
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	lock, err := locker.RLock(walPath)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	tickets, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query while shared lock held: %v", err)
	}

	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
}

// Contract: Query blocks when an exclusive lock is held, timing out after lockTimeout.
func Test_Query_Returns_Error_When_Exclusive_Lock_Held(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Hold an exclusive lock - Query should block and timeout
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	lock, err := locker.Lock(walPath)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	_, err = s.Query(t.Context(), nil)
	if err == nil {
		t.Fatal("expected error when exclusive lock held")
	}

	if !isDeadlineExceeded(err) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

// Contract: GetByPrefix uses a shared lock, so it succeeds when another shared lock is held.
func Test_GetByPrefix_Succeeds_When_Shared_Lock_Held(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket, err := store.NewTicket("Test Ticket", "task", "open", 2)
	if err != nil {
		t.Fatalf("new ticket: %v", err)
	}

	putTicket(t.Context(), t, s, ticket)

	// Reindex to populate SQLite
	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// Hold a shared lock - GetByPrefix should still succeed
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	lock, err := locker.RLock(walPath)
	if err != nil {
		t.Fatalf("acquire shared lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	// Use first 4 chars of short ID
	tickets, err := s.GetByPrefix(t.Context(), ticket.ShortID[:4])
	if err != nil {
		t.Fatalf("get by prefix while shared lock held: %v", err)
	}

	if len(tickets) != 1 {
		t.Fatalf("tickets = %d, want 1", len(tickets))
	}
}

// Contract: GetByPrefix blocks when an exclusive lock is held, timing out after lockTimeout.
func Test_GetByPrefix_Returns_Error_When_Exclusive_Lock_Held(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Hold an exclusive lock - GetByPrefix should block and timeout
	locker := fs.NewLocker(fs.NewReal())
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	lock, err := locker.Lock(walPath)
	if err != nil {
		t.Fatalf("acquire exclusive lock: %v", err)
	}

	defer func() { _ = lock.Close() }()

	_, err = s.GetByPrefix(t.Context(), "ABCD")
	if err == nil {
		t.Fatal("expected error when exclusive lock held")
	}

	if !isDeadlineExceeded(err) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
}

// Contract: Multiple concurrent readers can proceed without blocking each other.
func Test_Multiple_Readers_Succeed_When_Called_Concurrently(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir, store.WithLockTimeout(testLockTimeout))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket, err := store.NewTicket("Test Ticket", "task", "open", 2)
	if err != nil {
		t.Fatalf("new ticket: %v", err)
	}

	putTicket(t.Context(), t, s, ticket)

	// Reindex for Query
	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	const numReaders = 5

	errs := make(chan error, numReaders*2)

	// Spawn concurrent Get calls
	for range numReaders {
		go func() {
			_, err := s.Get(t.Context(), ticket.ID.String())
			errs <- err
		}()
	}

	// Spawn concurrent Query calls
	for range numReaders {
		go func() {
			_, err := s.Query(t.Context(), nil)
			errs <- err
		}()
	}

	// All should succeed
	for range numReaders * 2 {
		err := <-errs
		if err != nil {
			t.Errorf("concurrent read failed: %v", err)
		}
	}
}

func isDeadlineExceeded(err error) bool {
	return err != nil && (errors.Is(err, context.DeadlineExceeded) ||
		(err.Error() != "" && contains(err.Error(), "deadline exceeded")))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || s != "" && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
