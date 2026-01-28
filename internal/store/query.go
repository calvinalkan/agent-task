package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Summary is the SQLite-backed view returned by Query.
// It mirrors the derived index and never reads the filesystem.
type Summary struct {
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
// Zero values mean "no filter"; Priority uses 0 to mean "any".
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
// returning results. It may return [ErrWALCorrupt], [ErrWALReplay], or
// [ErrIndexUpdate] if recovery fails.
func (s *Store) Query(ctx context.Context, opts *QueryOptions) ([]Summary, error) {
	if ctx == nil {
		return nil, errors.New("query: context is nil")
	}

	if s == nil || s.sql == nil {
		return nil, errors.New("query: store is not open")
	}

	options := QueryOptions{}
	if opts != nil {
		options = *opts
	}

	if options.Limit < 0 || options.Offset < 0 {
		return nil, errors.New("query: limit/offset must be non-negative")
	}

	readLock, err := s.locker.RLock(s.walPath)
	if err != nil {
		return nil, fmt.Errorf("query: lock wal: %w", err)
	}

	walHasEntries, err := walHasData(s.wal)
	if err != nil {
		_ = readLock.Close()

		return nil, fmt.Errorf("query: %w", err)
	}

	if walHasEntries {
		_ = readLock.Close()
		readLock = nil

		writeLock, lockErr := s.locker.Lock(s.walPath)
		if lockErr != nil {
			return nil, fmt.Errorf("query: lock wal: %w", lockErr)
		}

		recoverErr := s.recoverWal(ctx, false)

		closeErr := writeLock.Close()
		if closeErr != nil && recoverErr == nil {
			recoverErr = fmt.Errorf("query: unlock wal: %w", closeErr)
		}

		if recoverErr != nil {
			return nil, recoverErr
		}

		readLock, err = s.locker.RLock(s.walPath)
		if err != nil {
			return nil, fmt.Errorf("query: lock wal: %w", err)
		}
	}

	defer func() {
		if readLock != nil {
			_ = readLock.Close()
		}
	}()

	clauses := make([]string, 0, 5)
	args := make([]any, 0, 7)

	if options.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, options.Status)
	}

	if options.Type != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, options.Type)
	}

	if options.Priority > 0 {
		clauses = append(clauses, "priority = ?")
		args = append(args, options.Priority)
	}

	if options.Parent != "" {
		clauses = append(clauses, "parent = ?")
		args = append(args, options.Parent)
	}

	if options.ShortIDPrefix != "" {
		clauses = append(clauses, "short_id LIKE ?")
		args = append(args, options.ShortIDPrefix+"%")
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT id, short_id, path, mtime_ns, status, type, priority,
			assignee, parent, created_at, closed_at, external_ref, title
		FROM tickets`)

	if len(clauses) > 0 {
		query.WriteString(" WHERE ")
		query.WriteString(strings.Join(clauses, " AND "))
	}

	query.WriteString(" ORDER BY id")

	if options.Limit > 0 {
		query.WriteString(" LIMIT ?")

		args = append(args, options.Limit)

		if options.Offset > 0 {
			query.WriteString(" OFFSET ?")

			args = append(args, options.Offset)
		}
	} else if options.Offset > 0 {
		// SQLite allows LIMIT -1 to indicate "no limit" while applying OFFSET.
		query.WriteString(" LIMIT -1 OFFSET ?")

		args = append(args, options.Offset)
	}

	rows, err := s.sql.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	defer func() { _ = rows.Close() }()

	summaries := make([]Summary, 0)
	ids := make([]string, 0)
	indexByID := make(map[string]int)

	for rows.Next() {
		var (
			summary   Summary
			assignee  sql.NullString
			parent    sql.NullString
			external  sql.NullString
			createdAt int64
			closedAt  sql.NullInt64
		)

		scanErr := rows.Scan(
			&summary.ID,
			&summary.ShortID,
			&summary.Path,
			&summary.MtimeNS,
			&summary.Status,
			&summary.Type,
			&summary.Priority,
			&assignee,
			&parent,
			&createdAt,
			&closedAt,
			&external,
			&summary.Title,
		)
		if scanErr != nil {
			return nil, fmt.Errorf("query: scan: %w", scanErr)
		}

		summary.Assignee = nullStringValue(assignee)
		summary.Parent = nullStringValue(parent)
		summary.ExternalRef = nullStringValue(external)
		summary.CreatedAt = time.Unix(createdAt, 0).UTC()
		summary.ClosedAt = nullTimePtr(closedAt)
		summary.BlockedBy = []string{}

		indexByID[summary.ID] = len(summaries)
		ids = append(ids, summary.ID)
		summaries = append(summaries, summary)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("query: rows: %w", err)
	}

	if len(ids) == 0 {
		return summaries, nil
	}

	blockers, err := queryBlockers(ctx, s.sql, ids)
	if err != nil {
		return nil, err
	}

	for _, blocker := range blockers {
		idx, ok := indexByID[blocker.ticketID]
		if !ok {
			continue
		}

		summaries[idx].BlockedBy = append(summaries[idx].BlockedBy, blocker.blockerID)
	}

	return summaries, nil
}

type blockerRow struct {
	ticketID  string
	blockerID string
}

func queryBlockers(ctx context.Context, db *sql.DB, ids []string) ([]blockerRow, error) {
	// ORDER BY keeps blocker lists stable across rebuilds and SQLite rowid reuse.
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = strings.TrimSuffix(placeholders, ",")

	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	query := strings.Builder{}
	query.WriteString(`
		SELECT ticket_id, blocker_id
		FROM ticket_blockers
		WHERE ticket_id IN (`)
	query.WriteString(placeholders)
	query.WriteString(`
		)
		ORDER BY ticket_id, blocker_id`)

	rows, err := db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("query blockers: %w", err)
	}

	defer func() { _ = rows.Close() }()

	blockers := make([]blockerRow, 0)

	for rows.Next() {
		var row blockerRow

		scanErr := rows.Scan(&row.ticketID, &row.blockerID)
		if scanErr != nil {
			return nil, fmt.Errorf("query blockers: scan: %w", scanErr)
		}

		blockers = append(blockers, row)
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("query blockers: rows: %w", err)
	}

	return blockers, nil
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}

	return value.String
}

func nullTimePtr(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}

	parsed := time.Unix(value.Int64, 0).UTC()

	return &parsed
}
