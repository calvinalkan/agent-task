// Package mddb provides a generic, markdown-based document store with SQLite indexing.
//
// # Overview
//
// mddb manages markdown documents with YAML frontmatter. It handles:
//   - Atomic file writes with WAL-based crash recovery
//   - File locking for concurrent read/write coordination
//   - SQLite indexing for fast queries
//   - Automatic reindexing on schema changes
//
// Users implement [Document] and provide a [Config] with parsing and schema
// callbacks. See [Config] for detailed documentation on all options.
//
// # Document Format
//
// Documents are stored as markdown files with YAML frontmatter:
//
//	---
//	id: 01948a7c-8e2a-7000-8000-000000000001
//	schema_version: 371013091
//	title: Example Document
//	status: open
//	priority: 1
//	---
//	Document body content here.
//
// # Reserved Frontmatter Fields
//
// mddb manages these fields automatically (do not include in [Document.Frontmatter]):
//   - id: Document identifier from [Document.ID]
//   - schema_version: Schema fingerprint at write time (diagnostics)
//   - title: Document title from [Document.Title]
//
// # SQLite Index
//
// The SQLite database is a derived cache, NOT the source of truth. Markdown
// files are authoritative. The index is rebuilt automatically on schema changes
// or explicitly via [MDDB.Reindex]. See [Config.SQLSchema] for schema definition
// and [Config.AfterRecreateSchema] for related tables.
//
// # Transactions
//
// All writes go through [Tx]. A transaction can include multiple Create/Update/Delete
// operations across different documents; last operation per ID wins. Commit applies
// all buffered operations as a single logical unit.
//
// # WAL and Crash Recovery
//
// Commits are staged through a write-ahead log (WAL) stored as JSON in ".mddb/wal".
// Commit order: WAL fsync (durable) → file writes → SQLite index update. If a crash
// happens after WAL fsync but before apply finishes, the WAL is replayed on the next
// [Open] or read. If replay fails, the WAL is readable JSON for manual recovery.
//
// # Tradeoffs / Notes
//
//   - Markdown files are the source of truth; SQLite is an ephemeral cache.
//   - Schema changes can trigger reindex; related tables must be recreated in hooks.
//   - WAL replay expects a compatible schema. If schema changes with a pending WAL,
//     recovery can fail; users may delete the WAL and reindex manually.
//   - Frontmatter parsing is a strict YAML subset (see [frontmatter] package docs).
//   - Single-writer model: one writer holds the exclusive lock; concurrent readers
//     are allowed, but writers block readers and readers block writers.
//
// # Example Usage
//
//	type Ticket struct {
//	    id, title, status, body string
//	    priority int64
//	}
//
//	func (t Ticket) ID() string    { return t.id }
//	func (t Ticket) Title() string { return t.title }
//	func (t Ticket) Body() string  { return t.body }
//	func (t Ticket) Frontmatter() frontmatter.Frontmatter {
//	    var fm frontmatter.Frontmatter
//	    fm.MustSet([]byte("status"), frontmatter.StringValue(t.status))
//	    fm.MustSet([]byte("priority"), frontmatter.IntValue(t.priority))
//	    return fm
//	}
//
//	db, err := mddb.Open(ctx, mddb.Config[Ticket]{
//	    BaseDir: ".data",
//	    DocumentFrom: func(doc mddb.IndexableDocument) (*Ticket, error) {
//	        status, _ := doc.Frontmatter.GetString([]byte("status"))
//	        priority, _ := doc.Frontmatter.GetInt([]byte("priority"))
//	        return &Ticket{
//	            id:       string(doc.ID),
//	            title:    string(doc.Title),
//	            body:     string(doc.Body),
//	            status:   status,
//	            priority: priority,
//	        }, nil
//	    },
//	    SQLSchema: mddb.NewBaseSQLSchema("tickets").
//	        Text("status", true).
//	        Int("priority", false),
//	    SQLColumnValues: func(doc mddb.IndexableDocument) []any {
//	        status, _ := doc.Frontmatter.GetString([]byte("status"))
//	        priority, _ := doc.Frontmatter.GetInt([]byte("priority"))
//	        return []any{status, priority}
//	    },
//	})
//
//	// Write
//	tx, _ := db.Begin(ctx)
//	tx.Create(&Ticket{id: "ABC123", title: "Fix bug", status: "open"})
//	tx.Commit(ctx)
//
//	// Read
//	ticket, _ := db.Get(ctx, "ABC123")
//
//	// Query
//	results, _ := mddb.Query(ctx, db, func(sqlDB *sql.DB) ([]string, error) {
//	    rows, _ := sqlDB.Query("SELECT id FROM tickets WHERE status = ?", "open")
//	    // ...
//	})
package mddb
