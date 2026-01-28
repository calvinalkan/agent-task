package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// QueryOptions defines optional SQLite filters for Query.
// Zero values mean "no filter" (Priority uses 0 to mean "any").
// Results are ordered by ID, with Limit/Offset applied after filters.
//
// For prefix-based ID lookups (short_id or UUID prefix), use [Store.GetByPrefix] instead.
type QueryOptions struct {
	// Status filters by exact status when non-empty.
	Status string
	// Type filters by exact ticket type when non-empty.
	Type string
	// Priority filters by exact priority when > 0.
	Priority int
	// Parent filters by exact parent ID when non-empty.
	Parent string
	// Limit caps the number of rows when > 0.
	Limit int
	// Offset skips rows when > 0.
	Offset int
}

// Query reads ticket metadata from SQLite. It avoids filesystem access so callers
// can list quickly after a rebuild or commit.
//
// Query returns TicketMeta without body content. Use [Store.Get] for full ticket data.
//
// Query acquires a shared WAL lock and replays any committed WAL entries before
// returning results. It may return [ErrWALCorrupt] or [ErrWALReplay] if
// recovery fails.
//
// For prefix-based ID lookups, use [Store.GetByPrefix] instead.
func (s *Store) Query(ctx context.Context, opts *QueryOptions) ([]Ticket, error) {
	if ctx == nil {
		return nil, errors.New("query: context is nil")
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return nil, errors.New("query: store is not open")
	}

	options := QueryOptions{}
	if opts != nil {
		options = *opts
	}

	if options.Limit < 0 || options.Offset < 0 {
		return nil, errors.New("query: limit/offset must be non-negative")
	}

	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	readLock, err := s.acquireReadLock(ctx, lockCtx)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	defer func() { _ = readLock.Close() }()

	tickets, err := queryTickets(ctx, s.sql, &options)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	return tickets, nil
}

// GetByPrefix resolves a short_id or UUID prefix to matching ticket metadata via SQLite.
// It returns up to 50 tickets whose short_id or full ID starts with the given prefix.
//
// The caller decides how to handle the result:
//   - Empty slice: no matches found
//   - Single ticket: unambiguous match, call [Store.Get] for full ticket with body
//   - Multiple tickets: ambiguous prefix, display list for user to disambiguate
//
// GetByPrefix acquires a shared WAL lock and replays any committed WAL entries
// before querying. It may return [ErrWALCorrupt] or [ErrWALReplay] if recovery fails.
//
// Example:
//
//	tickets, err := store.GetByPrefix(ctx, "A1B2")
//	if err != nil {
//	    return err
//	}
//	switch len(tickets) {
//	case 0:
//	    fmt.Println("no ticket found")
//	case 1:
//	    fmt.Printf("found: %s %s\n", tickets[0].ShortID, tickets[0].Title)
//	default:
//	    fmt.Println("ambiguous prefix, matches:")
//	    for _, t := range tickets {
//	        fmt.Printf("  %s %s %s\n", t.ShortID, t.Status, t.Title)
//	    }
//	}
func (s *Store) GetByPrefix(ctx context.Context, prefix string) ([]Ticket, error) {
	if ctx == nil {
		return nil, errors.New("get by prefix: context is nil")
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return nil, errors.New("get by prefix: store is not open")
	}

	if prefix == "" {
		return nil, errors.New("get by prefix: prefix is empty")
	}

	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	readLock, err := s.acquireReadLock(ctx, lockCtx)
	if err != nil {
		return nil, fmt.Errorf("get by prefix: %w", err)
	}

	defer func() { _ = readLock.Close() }()

	tickets, err := queryByPrefix(ctx, s.sql, prefix)
	if err != nil {
		return nil, fmt.Errorf("get by prefix: %w", err)
	}

	return tickets, nil
}

// Get reads a ticket directly from the filesystem for fresh data.
// It bypasses SQLite and always returns the current file contents.
//
// Get is strict: it only reads from the canonical path derived from the ID.
// It returns an error if the file does not exist or contains a different ID.
//
// Get acquires a shared WAL lock and replays any committed WAL entries before
// reading. It may return [ErrWALCorrupt] or [ErrWALReplay] if recovery fails.
func (s *Store) Get(ctx context.Context, id string) (*Ticket, error) {
	if ctx == nil {
		return nil, errors.New("get: context is nil")
	}

	if s == nil || s.sql == nil || s.wal == nil {
		return nil, errors.New("get: store is not open")
	}

	parsed, err := parseUUIDv7(id)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}

	relPath, err := pathFromID(parsed)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}

	lockCtx, cancel := context.WithTimeout(ctx, s.lockTimeout)
	defer cancel()

	readLock, err := s.acquireReadLock(ctx, lockCtx)
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}

	defer func() { _ = readLock.Close() }()

	ticket, err := s.readTicketFile(id, relPath)
	if err != nil {
		return nil, err
	}

	return ticket, nil
}

// readTicketFile reads and parses a ticket from its canonical path.
func (s *Store) readTicketFile(expectedID, relPath string) (*Ticket, error) {
	absPath := filepath.Join(s.dir, relPath)

	info, err := s.fs.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("get %s: not found", expectedID)
		}

		return nil, fmt.Errorf("get: stat %s: %w", relPath, err)
	}

	// Reject non-regular files (symlinks, devices, etc.) per spec.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("get %s: not found", expectedID)
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("get: read %s: %w", relPath, err)
	}

	ticket, err := parseTicketFile(data, relPath, info.ModTime().UnixNano())
	if err != nil {
		return nil, fmt.Errorf("get: %w", err)
	}

	return ticket, nil
}
