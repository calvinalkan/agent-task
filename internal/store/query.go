package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Ticket is the SQLite-backed view returned by Query.
// It mirrors the derived index and never reads the filesystem.
type Ticket struct {
	ID          string     // ID is the UUIDv7 stored in frontmatter.
	ShortID     string     // ShortID is the 12-char Crockford base32 identifier.
	Path        string     // Path is the canonical relative path to the ticket.
	MtimeNS     int64      // MtimeNS is the file modification time in nanoseconds.
	Status      string     // Status is the ticket status string (open/in_progress/closed).
	Type        string     // Type is the ticket type string (bug/feature/task/etc.).
	Priority    int64      // Priority is the numeric priority from frontmatter.
	Assignee    string     // Assignee is empty when unset in frontmatter.
	Parent      string     // Parent holds the parent ticket ID, empty when unset.
	CreatedAt   time.Time  // CreatedAt is the UTC timestamp from frontmatter.
	ClosedAt    *time.Time // ClosedAt is nil when the ticket is not closed.
	ExternalRef string     // ExternalRef is empty when unset in frontmatter.
	Title       string     // Title is parsed from the ticket body.
	BlockedBy   []string   // BlockedBy contains blocker IDs, sorted by ID.
}

// QueryOptions defines optional SQLite filters for Query.
// Zero values mean "no filter" (Priority uses 0 to mean "any").
// Results are ordered by ID, with Limit/Offset applied after filters.
//
// For prefix-based ID lookups (short_id or UUID prefix), use [Store.GetByPrefix] instead.
type QueryOptions struct {
	Status   string // Status filters by exact status when non-empty.
	Type     string // Type filters by exact ticket type when non-empty.
	Priority int    // Priority filters by exact priority when > 0.
	Parent   string // Parent filters by exact parent ID when non-empty.
	Limit    int    // Limit caps the number of rows when > 0.
	Offset   int    // Offset skips rows when > 0.
}

// Query reads ticket summaries from SQLite. It avoids filesystem access so callers
// can list quickly after a rebuild or commit.
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

// GetByPrefix resolves a short_id or UUID prefix to matching tickets via SQLite.
// It returns all tickets whose short_id or full ID starts with the given prefix.
//
// The caller decides how to handle the result:
//   - Empty slice: no matches found
//   - Single ticket: unambiguous match, use directly or call [Store.Get] for fresh filesystem data
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
