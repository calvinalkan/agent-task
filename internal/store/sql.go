package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// currentSchemaVersion is stored in SQLite's user_version pragma.
// Increment this whenever the schema changes (tables, columns, indices).
// A version mismatch triggers a full reindex on Open.
const currentSchemaVersion = 1

// openSqlite opens the derived index database and applies the configured pragmas.
func openSqlite(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errors.New("open sqlite: path is empty")
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	err = db.PingContext(ctx)
	if err != nil {
		_ = db.Close()

		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	err = applyPragmas(ctx, db)
	if err != nil {
		_ = db.Close()

		return nil, err
	}

	return db, nil
}

// sqliteBusyTimeout is the time SQLite waits when the database is locked.
// After this, operations return SQLITE_BUSY.
const sqliteBusyTimeout = 10000 // milliseconds

// applyPragmas configures the SQLite connection using a single batch statement.
func applyPragmas(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, fmt.Sprintf(`
		PRAGMA busy_timeout = %d;
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = FULL;
		PRAGMA mmap_size = 268435456;
		PRAGMA cache_size = -20000;
		PRAGMA temp_store = MEMORY;
	`, sqliteBusyTimeout))
	if err != nil {
		return fmt.Errorf("apply pragmas: %w", err)
	}

	return nil
}

// storedSchemaVersion reads the current SQLite PRAGMA user_version.
func storedSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}

	return version, nil
}

// dropAndRecreateSchema drops and recreates the derived index tables and indices.
func dropAndRecreateSchema(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		"DROP TABLE IF EXISTS ticket_blockers",
		"DROP TABLE IF EXISTS tickets",
		`CREATE TABLE tickets (
			id TEXT PRIMARY KEY,
			short_id TEXT NOT NULL,
			path TEXT NOT NULL,
			mtime_ns INTEGER NOT NULL,
			status TEXT NOT NULL,
			type TEXT NOT NULL,
			priority INTEGER NOT NULL,
			assignee TEXT,
			parent TEXT,
			created_at INTEGER NOT NULL,
			closed_at INTEGER,
			external_ref TEXT,
			title TEXT NOT NULL
		) WITHOUT ROWID`,
		`CREATE TABLE ticket_blockers (
			ticket_id TEXT NOT NULL,
			blocker_id TEXT NOT NULL,
			PRIMARY KEY (ticket_id, blocker_id)
		) WITHOUT ROWID`,
		"CREATE INDEX idx_status_priority ON tickets(status, priority)",
		"CREATE INDEX idx_status_type ON tickets(status, type)",
		"CREATE INDEX idx_parent ON tickets(parent)",
		"CREATE INDEX idx_short_id ON tickets(short_id)",
		"CREATE INDEX idx_blocker ON ticket_blockers(blocker_id)",
	}

	for i, stmt := range statements {
		_, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return fmt.Errorf("schema statement %d: %w", i+1, err)
		}
	}

	return nil
}

// ticketInserter holds prepared statements for inserting tickets and blockers.
type ticketInserter struct {
	insertTicket  *sql.Stmt
	insertBlocker *sql.Stmt
}

// prepareTicketInserter creates prepared statements for ticket insertion within a transaction.
func prepareTicketInserter(ctx context.Context, tx *sql.Tx) (*ticketInserter, error) {
	var insertTicket, insertBlocker *sql.Stmt

	success := false

	defer func() {
		if !success {
			if insertTicket != nil {
				_ = insertTicket.Close()
			}

			if insertBlocker != nil {
				_ = insertBlocker.Close()
			}
		}
	}()

	var err error

	insertTicket, err = tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO tickets (
			id,
			short_id,
			path,
			mtime_ns,
			status,
			type,
			priority,
			assignee,
			parent,
			created_at,
			closed_at,
			external_ref,
			title
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare insert: %w", err)
	}

	insertBlocker, err = tx.PrepareContext(ctx, `
		INSERT INTO ticket_blockers (ticket_id, blocker_id) VALUES (?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare blocker insert: %w", err)
	}

	success = true

	return &ticketInserter{
		insertTicket:  insertTicket,
		insertBlocker: insertBlocker,
	}, nil
}

// Close releases the prepared statements.
func (ti *ticketInserter) Close() {
	if ti.insertTicket != nil {
		_ = ti.insertTicket.Close()
	}

	if ti.insertBlocker != nil {
		_ = ti.insertBlocker.Close()
	}
}

// Insert inserts a ticket and its blockers. It clears existing blockers first.
func (ti *ticketInserter) Insert(ctx context.Context, tx *sql.Tx, entry *Ticket) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM ticket_blockers WHERE ticket_id = ?", entry.ID)
	if err != nil {
		return fmt.Errorf("clear blockers %s: %w", entry.ID, err)
	}

	assignee := sql.NullString{String: entry.Assignee, Valid: entry.Assignee != ""}
	parent := sql.NullString{String: entry.Parent, Valid: entry.Parent != ""}
	external := sql.NullString{String: entry.ExternalRef, Valid: entry.ExternalRef != ""}
	createdAt := entry.CreatedAt.Unix()

	closedAt := sql.NullInt64{}
	if entry.ClosedAt != nil {
		closedAt = sql.NullInt64{Int64: entry.ClosedAt.Unix(), Valid: true}
	}

	_, err = ti.insertTicket.ExecContext(
		ctx,
		entry.ID,
		entry.ShortID,
		entry.Path,
		entry.MtimeNS,
		entry.Status,
		entry.Type,
		entry.Priority,
		assignee,
		parent,
		createdAt,
		closedAt,
		external,
		entry.Title,
	)
	if err != nil {
		return fmt.Errorf("insert ticket %s: %w", entry.ID, err)
	}

	for _, blocker := range entry.BlockedBy {
		_, err = ti.insertBlocker.ExecContext(ctx, entry.ID, blocker)
		if err != nil {
			return fmt.Errorf("insert blocker %s: %w", entry.ID, err)
		}
	}

	return nil
}

// deleteTicket removes a ticket and its blockers from the index.
func deleteTicket(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM ticket_blockers WHERE ticket_id = ?", id)
	if err != nil {
		return fmt.Errorf("delete blockers %s: %w", id, err)
	}

	_, err = tx.ExecContext(ctx, "DELETE FROM tickets WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete ticket %s: %w", id, err)
	}

	return nil
}

// buildTicketQuery constructs the SQL query and args for ticket listing.
// When limit/offset is specified, it uses a subquery to paginate tickets
// before joining blockers (since LIMIT applies to rows, not distinct tickets).
func buildTicketQuery(options *QueryOptions) (string, []any) {
	var (
		clauses []string
		args    []any
	)

	if options.Status != "" {
		clauses = append(clauses, "t.status = ?")
		args = append(args, options.Status)
	}

	if options.Type != "" {
		clauses = append(clauses, "t.type = ?")
		args = append(args, options.Type)
	}

	if options.Priority > 0 {
		clauses = append(clauses, "t.priority = ?")
		args = append(args, options.Priority)
	}

	if options.Parent != "" {
		clauses = append(clauses, "t.parent = ?")
		args = append(args, options.Parent)
	}

	if options.ShortIDPrefix != "" {
		clauses = append(clauses, "t.short_id LIKE ?")
		args = append(args, options.ShortIDPrefix+"%")
	}

	whereClause := ""
	if len(clauses) > 0 {
		whereClause = " WHERE " + strings.Join(clauses, " AND ")
	}

	needsSubquery := options.Limit > 0 || options.Offset > 0

	if !needsSubquery {
		return `
		SELECT t.id, t.short_id, t.path, t.mtime_ns, t.status, t.type, t.priority,
			t.assignee, t.parent, t.created_at, t.closed_at, t.external_ref, t.title,
			b.blocker_id
		FROM tickets t
		LEFT JOIN ticket_blockers b ON t.id = b.ticket_id` + whereClause + `
		ORDER BY t.id, b.blocker_id`, args
	}

	// Build subquery with pagination, then join blockers.
	subquery := `
		SELECT t.id, t.short_id, t.path, t.mtime_ns, t.status, t.type, t.priority,
			t.assignee, t.parent, t.created_at, t.closed_at, t.external_ref, t.title
		FROM tickets t` + whereClause + `
		ORDER BY t.id`

	if options.Limit > 0 {
		subquery += " LIMIT ?"

		args = append(args, options.Limit)
	} else {
		// SQLite requires LIMIT with OFFSET; -1 means no limit.
		subquery += " LIMIT -1"
	}

	if options.Offset > 0 {
		subquery += " OFFSET ?"

		args = append(args, options.Offset)
	}

	query := `
		SELECT t.id, t.short_id, t.path, t.mtime_ns, t.status, t.type, t.priority,
			t.assignee, t.parent, t.created_at, t.closed_at, t.external_ref, t.title,
			b.blocker_id
		FROM (` + subquery + `) t
		LEFT JOIN ticket_blockers b ON t.id = b.ticket_id
		ORDER BY t.id, b.blocker_id`

	return query, args
}

// queryTickets builds and executes a ticket query with the given options.
// It uses a LEFT JOIN to fetch tickets and blockers in a single query.
func queryTickets(ctx context.Context, db *sql.DB, options *QueryOptions) ([]Ticket, error) {
	query, args := buildTicketQuery(options)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}

	defer func() { _ = rows.Close() }()

	var (
		tickets []Ticket
		current *Ticket
	)

	for rows.Next() {
		var (
			id        string
			shortID   string
			path      string
			mtimeNS   int64
			status    string
			ticketTyp string
			priority  int64
			assignee  sql.NullString
			parent    sql.NullString
			createdAt int64
			closedAt  sql.NullInt64
			external  sql.NullString
			title     string
			blockerID sql.NullString
		)

		scanErr := rows.Scan(
			&id,
			&shortID,
			&path,
			&mtimeNS,
			&status,
			&ticketTyp,
			&priority,
			&assignee,
			&parent,
			&createdAt,
			&closedAt,
			&external,
			&title,
			&blockerID,
		)
		if scanErr != nil {
			return nil, fmt.Errorf("scan: %w", scanErr)
		}

		if current == nil || current.ID != id {
			tickets = append(tickets, Ticket{
				ID:          id,
				ShortID:     shortID,
				Path:        path,
				MtimeNS:     mtimeNS,
				Status:      status,
				Type:        ticketTyp,
				Priority:    priority,
				Assignee:    nullStringValue(assignee),
				Parent:      nullStringValue(parent),
				CreatedAt:   time.Unix(createdAt, 0).UTC(),
				ClosedAt:    nullTimePtr(closedAt),
				ExternalRef: nullStringValue(external),
				Title:       title,
				BlockedBy:   []string{},
			})
			current = &tickets[len(tickets)-1]
		}

		if blockerID.Valid {
			current.BlockedBy = append(current.BlockedBy, blockerID.String)
		}
	}

	err = rows.Err()
	if err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	if tickets == nil {
		tickets = []Ticket{}
	}

	return tickets, nil
}

// nullStringValue extracts a string from sql.NullString, returning empty if not valid.
func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}

	return value.String
}

// nullTimePtr converts a Unix timestamp to *time.Time, returning nil if not valid.
func nullTimePtr(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}

	parsed := time.Unix(value.Int64, 0).UTC()

	return &parsed
}
