// Package mddb provides a generic, file-based document store with SQLite indexing.
//
// # Overview
//
// Store manages markdown documents with YAML frontmatter. It handles:
//   - Atomic file writes with WAL-based crash recovery
//   - File locking for concurrent read/write coordination
//   - SQLite indexing for fast queries
//   - Automatic reindexing on schema version changes
//
// Users implement the [Document] interface and provide a [Config] with parsing
// and schema callbacks. Store handles the markdown format, reserved fields,
// and all I/O coordination.
//
// # Document Format
//
// Documents are stored as markdown files with YAML frontmatter:
//
//	---
//	id: 01948a7c-8e2a-7000-8000-000000000001
//	schema_version: 10001
//	title: Example Document
//	status: open
//	priority: 1
//	---
//	Document body content here.
//
// # Reserved Frontmatter Fields
//
// Store manages these frontmatter fields automatically:
//   - id: Document identifier string (user-defined format)
//   - schema_version: Combined store + user version for reindex detection
//
// Users must NOT include these in [Document.Frontmatter]. Store injects them
// on write and extracts them on read, passing the parsed id to [Config.Parse].
//
// # Data Directory
//
// Store operates on a data directory passed to [Open]. All document paths
// returned by [Document.RelPath] are relative to this directory. For example:
//
//	mddb.Open(ctx, "/home/user/data", cfg)
//
//	doc.RelPath() returns "2026/01-28/ABC123.md"
//	Absolute path: /home/user/data/2026/01-28/ABC123.md
//
// mddb creates a ".mddb" subdirectory for internal files:
//
//	/home/user/data/.mddb/index.sqlite  -- SQLite index
//	/home/user/data/.mddb/wal           -- Write-ahead log
//
// # SQLite Index (Ephemeral)
//
// The SQLite database is a derived index, NOT the source of truth.
// Markdown files are authoritative. The index can be deleted and rebuilt
// at any time via [MDDB.Reindex]. Store automatically rebuilds when:
//   - Schema version changes (internal or user)
//   - Index is missing or corrupted
//
// # Required SQLite Columns
//
// User's [Config.RecreateIndex] must create a table with these base columns:
//
//	id        TEXT PRIMARY KEY  -- Document ID string
//	short_id  TEXT NOT NULL     -- Short identifier for prefix search
//	path      TEXT NOT NULL     -- Relative file path
//	mtime_ns  INTEGER NOT NULL  -- File modification time (nanoseconds)
//	title     TEXT NOT NULL     -- Document title for listings
//
// Users add their own columns (status, priority, body, etc.) as needed.
// Store uses these base columns for [MDDB.GetByPrefix] and cache invalidation.
//
// # Path Layout
//
// Each consumer defines their own path derivation from the document ID.
// Store validates that:
//   - Path ends with ".md"
//   - Path does not escape data directory (no ".." traversal)
//   - File location matches doc.RelPath() after parsing
//
// Example layouts (user's choice):
//
//	tk:     2026/01-28/ABC123.md  (date-based from time-ordered ID)
//	flat:   ABC123.md             (all files in root)
//	custom: bugs/ABC123.md        (category-based)
//
// # Short ID Format
//
// Each consumer defines their own short ID derivation from the ID.
// Store uses short_id for prefix search but does not mandate any format.
//
// Common approaches:
//   - Hash of the ID string
//   - Truncated ID prefix
//   - Custom encoding scheme
//
// # Schema Versioning
//
// Store combines an internal version with user's [Config.SchemaVersion]:
//
//	combined = internalVersion * 10000 + userVersion
//
// On [Open], if the stored PRAGMA user_version differs from the combined
// version, Store triggers a full reindex. Store does not validate individual
// document schema_version values; user callbacks should handle version
// differences as needed.
//
// # Callback Lifecycle
//
// Store calls user callbacks at specific points:
//
//	Open / Reindex:
//	  1. [Config.RecreateIndex] - Drop and recreate tables/indexes (in transaction)
//	  2. [Config.Prepare] - Create prepared statements for batch insert
//	  3. [PreparedStatements.Upsert] - Called for each document
//	  4. [PreparedStatements.Close] - Release statements
//
//	Get (by full ID):
//	  1. Read file from disk at doc.RelPath() relative to data dir
//	  2. Parse frontmatter, extract id/schema_version
//	  3. [Config.Parse] - Build document from frontmatter + body
//	  4. Validate doc.RelPath() matches actual file path
//
//	GetByPrefix:
//	  1. Query SQLite for matching id/short_id prefixes
//	  2. Return [BaseMeta] (no user callback, base columns only)
//
//	Query (user's custom queries):
//	  1. Acquire read lock, replay WAL if needed
//	  2. User callback receives *sql.DB for querying
//	  3. Release lock when callback returns
//
//	Tx.Put:
//	  1. [Document.Validate] - Check document integrity
//	  2. Buffer operation (no disk write yet)
//
//	Tx.Commit:
//	  1. Write WAL with all operations (crash recovery point)
//	  2. For each Put: [Document.Frontmatter] + [Document.Body] - Marshal to file
//	  3. Write files atomically to data dir
//	  4. [Config.Prepare] - Create prepared statements
//	  5. [PreparedStatements.Upsert] / [PreparedStatements.Delete] - Update index
//	  6. [PreparedStatements.Close] - Release statements
//	  7. Truncate WAL
//
// # Concurrency
//
// Store uses file locking for coordination:
//   - Readers acquire shared locks and replay any pending WAL
//   - Writers acquire exclusive locks via [MDDB.Begin]
//   - WAL ensures crash recovery: incomplete writes are replayed on next open
//
// # Example Usage
//
//	// Define your document type
//	Ticket struct
//	    ID       string
//	    ShortID  string
//	    Path     string
//	    MtimeNS  int64
//	    Title    string
//	    Status   string
//	    Priority int64
//	    Body     string
//	}
//
//	func (t *Ticket) ID() string             { return t.ID }
//	func (t *Ticket) RelPath() string        { return t.Path }
//	func (t *Ticket) ShortID() string       { return t.ShortID }
//	func (t *Ticket) DocMtimeNS() int64      { return t.MtimeNS }
//	func (t *Ticket) DocTitle() string       { return t.Title }
//	func (t *Ticket) Body() string           { return t.Body }
//	func (t *Ticket) Validate() error        { /* ... */ }
//	func (t *Ticket) Frontmatter() frontmatter.Frontmatter {
//	    return frontmatter.Frontmatter{
//	        "title":    frontmatter.StringValue(t.Title),
//	        "status":   frontmatter.StringValue(t.Status),
//	        "priority": frontmatter.IntValue(t.Priority),
//	    }
//	}
//
//	// Open with config
//	s, err := mddb.Open(ctx, ".data", mddb.Config[MyDoc]{
//	    SchemaVersion: 1,
//	    Parse:         parseDoc,
//	    RecreateIndex: createIndex,
//	    Prepare:       prepareStatements,
//	}, mddb.WithTableName("documents"))
//
//	// Query with locking
//	docs, err := mddb.Query(ctx, s, func(db *sql.DB) ([]MyDoc, error) {
//	    // Your custom query here
//	})
package mddb

import (
	"context"
	"database/sql"
	"time"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

// internalSchemaVersion is the store's internal schema version.
// Bump this when changing store-managed tables, indexes, or base column requirements.
const internalSchemaVersion = 1

// Document defines the interface that user document types must implement.
//
// Store calls these methods for:
//   - ID, RelPath, ShortID: Identifiers for storage and lookup
//   - MtimeNS: Cache invalidation (set during [Config.Parse])
//   - Title: Display in [BaseMeta] for listings
//   - Frontmatter: User fields for YAML serialization (exclude id, schema_version)
//   - Body: Markdown content after frontmatter
//   - Validate: Called before writes to ensure document integrity
type Document interface {
	// ID returns the document's unique identifier.
	//
	// Must be non-empty and unique. User chooses format:
	//   - UUIDv7/ULID for distributed systems (recommended)
	//   - Auto-increment integers (query via [Tx.DB] before Put)
	//   - Custom formats ("TICKET-2024-001")
	//
	// Stored as string in frontmatter. SQLite column type is user's choice.
	ID() string

	// RelPath returns the relative file path for this document.
	//
	// Path is relative to the data directory passed to [Open]. For example,
	// if Open is called with "/home/user/data" and RelPath returns
	// "2026/01-28/ABC123.md", the absolute file location is:
	//
	//	/home/user/data/2026/01-28/ABC123.md
	//
	// User is responsible for deriving path from ID. Store validates:
	//   - Path ends with ".md"
	//   - Path does not escape the data directory (no ".." traversal)
	//   - File exists at this path (for reads)
	//   - Actual file location matches doc.RelPath() after parsing
	//
	// Example layouts (user's choice):
	//   - "2026/01-28/ABC123.md" (date-based directories)
	//   - "ABC123.md" (flat, all files in root)
	//   - "bugs/ABC123.md" (category-based)
	RelPath() string

	// ShortID returns a short identifier derived from the ID.
	//
	// User is responsible for format and derivation. Store uses this for:
	//   - Prefix search in [Store.GetByPrefix]
	//   - Human-friendly display in [BaseMeta]
	//
	// Common approaches:
	//   - Hash of the ID string
	//   - Truncated ID prefix
	//   - Custom encoding
	ShortID() string

	// MtimeNS returns the file modification time in nanoseconds.
	// Set during [Config.Parse] from file stat, used for cache invalidation.
	MtimeNS() int64

	// Title returns the document title for listings and disambiguation.
	// Stored in SQLite and returned in [BaseMeta].
	Title() string

	// Frontmatter returns user-defined frontmatter fields for serialization.
	//
	// Do NOT include "id" or "schema_version" - these are reserved fields
	// that store manages automatically. Store injects them when writing files.
	//
	// Example:
	//
	//	func (t *Ticket) Frontmatter() frontmatter.Frontmatter {
	//	    return frontmatter.Frontmatter{
	//	        "title":    frontmatter.StringValue(t.Title),
	//	        "status":   frontmatter.StringValue(t.Status),
	//	        "priority": frontmatter.IntValue(t.Priority),
	//	    }
	//	}
	Frontmatter() frontmatter.Frontmatter

	// Body returns the markdown content after the frontmatter block.
	Body() string

	// Validate checks document integrity before writes.
	// Return an error to prevent invalid documents from being stored.
	// Called by [Tx.Put] before buffering the operation.
	Validate() error
}

// BaseMeta contains the base fields returned by [MDDB.GetByPrefix].
// These correspond to the required SQLite columns that all documents must have.
// Use [MDDB.Get] to retrieve the full document with body and custom fields.
type BaseMeta struct {
	// ID is the document's unique identifier.
	ID string

	// ShortID is the short identifier for human-friendly references.
	ShortID string

	// Path is the relative file path (relative to data directory).
	Path string

	// MtimeNS is the file modification time in nanoseconds.
	MtimeNS int64

	// Title is the document title for display in listings.
	Title string
}

// Config provides all settings and callbacks for document storage.
type Config[T Document] struct {
	// Dir is the data directory where documents are stored.
	// Required.
	Dir string

	// TableName is the SQLite table name. Default: "documents".
	TableName string

	// LockTimeout is max wait for WAL locks. Default: 10s.
	LockTimeout time.Duration

	// SchemaVersion is the user's schema version.
	// Bump when changing table structure, indexes, or document format.
	// Combined with internal version: (internal * 10000) + user.
	// Mismatch triggers full reindex on [Open].
	SchemaVersion int

	// FrontmatterLineLimit controls YAML frontmatter line parsing.
	// Use 0 to disable the limit entirely.
	FrontmatterLineLimit int

	// Parse builds a document from parsed frontmatter and body.
	//
	// Called by:
	//   - [Store.Get] when reading a document by ID
	//   - [Store.Reindex] when scanning all files
	//
	// Store handles:
	//   - Parsing the markdown file (YAML frontmatter + body)
	//   - Extracting reserved fields (id, schema_version)
	//   - Providing the file's mtime for cache invalidation
	//
	// User handles:
	//   - Reading custom fields from frontmatter
	//   - Deriving path and short_id from the id (user-defined format)
	//   - Storing mtime in the document
	//   - Returning a fully populated document
	//
	// Parameters:
	//   - id: Document ID from frontmatter (non-empty string)
	//   - fm: All frontmatter fields (read your custom fields, ignore reserved)
	//   - body: Markdown content after frontmatter
	//   - mtimeNS: File modification time from stat
	Parse func(id string, fm frontmatter.Frontmatter, body string, mtimeNS int64) (*T, error)

	// RecreateIndex drops and recreates the SQLite index tables.
	//
	// Called by:
	//   - [Open] when schema version mismatches
	//   - [Store.Reindex] to rebuild from scratch
	//
	// IMPORTANT: The SQLite index is ephemeral derived data.
	// Markdown files are the source of truth. This callback MUST:
	//   1. DROP existing tables (if any)
	//   2. CREATE fresh tables and indexes
	//
	// Required base columns (store depends on these):
	//
	//	id        TEXT PRIMARY KEY  -- Document ID (or INTEGER for auto-increment)
	//	short_id  TEXT NOT NULL     -- For prefix search
	//	path      TEXT NOT NULL     -- Relative file path
	//	mtime_ns  INTEGER NOT NULL  -- Cache invalidation
	//	title     TEXT NOT NULL     -- For [BaseMeta] listings
	//
	// Required index:
	//
	//	CREATE INDEX idx_short_id ON {tableName}(short_id);
	//
	// Example:
	//
	//	func RecreateIndex(ctx context.Context, tx *sql.Tx, tableName string) error {
	//	    stmts := []string{
	//	        fmt.Sprintf("DROP TABLE IF EXISTS %s", tableName),
	//	        fmt.Sprintf(`CREATE TABLE %s (
	//	            id        TEXT PRIMARY KEY,
	//	            short_id  TEXT NOT NULL,
	//	            path      TEXT NOT NULL,
	//	            mtime_ns  INTEGER NOT NULL,
	//	            title     TEXT NOT NULL,
	//	            status    TEXT NOT NULL,
	//	            priority  INTEGER NOT NULL,
	//	            body      TEXT NOT NULL
	//	        ) WITHOUT ROWID`, tableName),
	//	        fmt.Sprintf("CREATE INDEX idx_short_id ON %s(short_id)", tableName),
	//	    }
	//	    for _, stmt := range stmts {
	//	        if _, err := tx.ExecContext(ctx, stmt); err != nil {
	//	            return err
	//	        }
	//	    }
	//	    return nil
	//	}
	//
	// The tableName parameter comes from [WithTableName] (default: "documents").
	RecreateIndex func(ctx context.Context, tx *sql.Tx, tableName string) error

	// Prepare creates prepared statements for batch upsert/delete operations.
	//
	// Called by:
	//   - [Tx.Commit] before applying buffered operations
	//   - [Store.Reindex] before inserting all documents
	//
	// Returns [PreparedStatements] used for all operations within the
	// transaction, then closed. Use prepared statements for performance -
	// reindex may process millions of documents.
	//
	// The tableName parameter comes from [WithTableName] (default: "documents").
	Prepare func(ctx context.Context, tx *sql.Tx, tableName string) (PreparedStatements[T], error)
}

// PreparedStatements handles batch upsert/delete with prepared SQL statements.
// Created via [Config.Prepare], used within a single transaction, then closed.
//
// Implementations should prepare INSERT OR REPLACE and DELETE statements,
// reusing them for each document to maximize performance during batch operations.
type PreparedStatements[T Document] interface {
	// Upsert inserts or replaces a document in the index.
	//
	// Called by:
	//   - [Tx.Commit] for Put operations
	//   - [Store.Reindex] for each scanned document
	//
	// Must populate all base columns (id, short_id, path, mtime_ns, title)
	// plus user columns. Use INSERT OR REPLACE for upsert semantics.
	Upsert(ctx context.Context, doc *T) error

	// Delete removes a document from the index.
	//
	// Called by:
	//   - [Tx.Commit] for Delete operations
	//
	// Must delete from main table and any related tables.
	// Deleting a non-existent document should succeed (idempotent).
	Delete(ctx context.Context, id string) error

	// Close releases prepared statement resources.
	// Called when the transaction completes (commit or rollback).
	Close() error
}

// combinedSchemaVersion returns the combined internal + user schema version.
// Used for PRAGMA user_version to detect schema changes requiring reindex.
func combinedSchemaVersion(userVersion int) int {
	return internalSchemaVersion*10000 + userVersion
}
