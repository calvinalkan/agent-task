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
type QueryOptions struct {
	Status        string // Status filters by exact status when non-empty.
	Type          string // Type filters by exact ticket type when non-empty.
	Priority      int    // Priority filters by exact priority when > 0.
	Parent        string // Parent filters by exact parent ID when non-empty.
	ShortIDPrefix string // ShortIDPrefix filters by short ID prefix when non-empty.
	Limit         int    // Limit caps the number of rows when > 0.
	Offset        int    // Offset skips rows when > 0.
}

// Query reads ticket summaries from SQLite. It avoids filesystem access so callers
// can list quickly after a rebuild or commit.
//
// Query acquires a shared WAL lock and replays any committed WAL entries before
// returning results. It may return [ErrWALCorrupt] or [ErrWALReplay] if
// recovery fails.
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

	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()

	// Stage: take a shared lock so we never read SQLite while a commit is mid-flight.
	readLock, err := s.locker.RLockWithTimeout(lockCtx, s.lockPath)
	if err != nil {
		return nil, fmt.Errorf("query: lock wal: %w", err)
	}

	var walSize int64

	// If the WAL has data, we need to recover under an exclusive lock. This loop:
	//  1) checks the WAL under a shared lock,
	//  2) releases the shared lock so we can upgrade to exclusive,
	//  3) re-checks after the upgrade to avoid duplicate recoveries, and
	//  4) reacquires the shared lock before querying to avoid mid-commit reads.
	// We loop to handle the tiny window where another writer/recovery happens
	// between releasing the exclusive lock and reacquiring the shared one.
	for {
		var statErr error

		walSize, statErr = s.walSize()
		if statErr != nil {
			_ = readLock.Close()

			return nil, fmt.Errorf("query: wal stat: %w", statErr)
		}

		if walSize == 0 {
			break
		}

		closeErr := readLock.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("query: unlock wal: %w", closeErr)
		}

		readLock = nil

		// Stage: upgrade to exclusive lock for recovery.
		writeLock, lockErr := s.locker.LockWithTimeout(lockCtx, s.lockPath)
		if lockErr != nil {
			return nil, fmt.Errorf("query: lock wal: %w", lockErr)
		}

		recoverErr := s.recoverWalLocked(ctx)
		if recoverErr != nil {
			_ = writeLock.Close()

			return nil, recoverErr
		}

		closeErr = writeLock.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("query: unlock wal: %w", closeErr)
		}

		// Stage: reacquire shared lock to guard against concurrent commits while querying.
		readLock, err = s.locker.RLockWithTimeout(lockCtx, s.lockPath)
		if err != nil {
			return nil, fmt.Errorf("query: lock wal: %w", err)
		}
	}

	defer func() {
		if readLock != nil {
			_ = readLock.Close()
		}
	}()

	tickets, err := queryTickets(ctx, s.sql, &options)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	return tickets, nil
}
