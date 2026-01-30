package mddb

import (
	"context"
	"database/sql"
	"time"

	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

// Document is the minimal interface for storable documents.
//
// This is the contract between your document type and mddb. Your struct can have
// any additional fields and methods - these are just the ones mddb needs to:
//   - Identify and locate documents (ID)
//   - Display in listings (Title)
//   - Serialize to markdown files (Frontmatter, Body)
//
// mddb calls these methods during [Tx.Create] / [Tx.Update] to marshal your document to disk.
// The resulting file looks like:
//
//	---
//	id: <from ID()>
//	schema_version: <auto>
//	title: <from Title()>
//	<your frontmatter fields>
//	---
//	<from Body()>
type Document interface {
	// ID returns the document's unique identifier.
	//
	// Used for:
	//   - Primary key in SQLite index
	//   - File path derivation via [Config.RelPathFromID]
	//   - Short ID derivation via [Config.ShortIDFromID]
	//   - Written to frontmatter as "id" field
	//
	// Must be non-empty, unique, and stable (same doc = same ID always).
	// Format is your choice: UUIDv7, ULID, "TICKET-001", auto-increment, etc.
	ID() string

	// Title returns the document title for display.
	//
	// Used for:
	//   - Written to frontmatter as "title" field
	//   - Stored in SQLite for fast listing queries
	//   - Returned in [GetPrefixRow] for prefix search results
	//
	// Useful for disambiguation when multiple docs match a prefix search.
	Title() string

	// Frontmatter returns YOUR custom fields for YAML serialization.
	//
	// This is the FILE representation - human-readable, supports rich structures:
	//   - Scalars: status: open, priority: 1
	//   - Lists: tags: [bug, urgent]
	//   - Objects: metadata: {created: 2024-01-01, author: alice}
	//
	// The INDEX representation is separate - see [Config.SQLSchema] docs.
	// You might store tags: [a, b, c] here but index them in a normalized tags table.
	//
	// mddb writes these to the frontmatter block along with reserved fields
	// (id, schema_version, title). On read, you get them back in
	// [IndexableDocument.Frontmatter] to reconstruct your document.
	//
	// Do NOT include reserved fields - mddb adds them automatically.
	//
	// Example:
	//
	//	func (t Ticket) Frontmatter() frontmatter.Frontmatter {
	//	    var fm frontmatter.Frontmatter
	//	    fm.MustSet([]byte("status"), frontmatter.StringValue(t.Status))
	//	    fm.MustSet([]byte("priority"), frontmatter.IntValue(t.Priority))
	//	    return fm
	//	}
	Frontmatter() frontmatter.Frontmatter

	// Body returns the markdown content after the frontmatter block.
	//
	// This is the main document content - task descriptions, notes, etc.
	// Written verbatim to the file after the closing "---" delimiter.
	//
	// Can be empty. Trailing newlines are normalized on write.
	Body() string
}

// Config provides all settings and callbacks for document storage.
//
// mddb maintains two representations:
//   - FILES: Markdown + YAML frontmatter (source of truth)
//   - INDEX: SQLite cache for queries (ephemeral, see [Config.SQLSchema])
type Config[T Document] struct {
	// BaseDir is the root directory for document storage.
	//
	// Documents are stored as markdown files in this directory (or subdirectories
	// based on RelPathFromID). mddb creates a ".mddb" subdirectory for internal
	// files (SQLite index, WAL).
	//
	// Created automatically if it doesn't exist.
	BaseDir string

	// DocumentFrom builds a user document from parsed file data.
	//
	// Called by [MDDB.Get] and [MDDB.Reindex] after parsing markdown files.
	// All [IndexableDocument] fields are borrowed from the file buffer - convert
	// to string for storage in your document type.
	//
	// Example:
	//
	//	DocumentFrom: func(doc mddb.IndexableDocument) (*Ticket, error) {
	//	    status, _ := doc.Frontmatter.GetString([]byte("status"))
	//	    return &Ticket{
	//	        id:      string(doc.ID),
	//	        title:   string(doc.Title),
	//	        body:    string(doc.Body),
	//	        mtimeNS: doc.MtimeNS,
	//	        status:  status,
	//	    }, nil
	//	}
	//
	DocumentFrom func(doc IndexableDocument) (*T, error)

	//
	//
	// -----------------------------------------------
	// OPTIONAL SETTINGS (SENSIBLE DEFAULTS PROVIDED)
	// -----------------------------------------------
	//
	//

	// SQLSchema defines the SQLite table structure for the document index.
	//
	// The index representation is INDEPENDENT from the file representation:
	//
	//	Frontmatter (YAML)          SQLite Index
	//	------------------          ------------
	//	tags: [bug, urgent]    →    separate 'tags' table (normalized)
	//	priority: 1            →    priority INTEGER column
	//	metadata:              →    maybe not indexed at all
	//	  created: 2024-01-01
	//
	// This decoupling lets you:
	//   - Store rich data in files (lists, objects, nested structures)
	//   - Index only what you need to query
	//   - Use different representations (list in YAML → normalized table in SQL)
	//   - Skip indexing entirely if you only need file storage
	//
	// IMPORTANT: The SQLite index is fully ephemeral - a derived cache, NOT a
	// data store. Markdown files are the source of truth. mddb will drop and
	// recreate the entire database at various points:
	//   - SQLSchema fingerprint changes (columns, indexes modified)
	//   - Explicit [MDDB.Reindex] calls
	//   - Index corruption or missing database file
	//
	// Rebuilding is fast even for 1m+ documents. Do NOT store any
	// long-term data in the index that isn't derived from the markdown files.
	// Use [Config.AfterRecreateSchema] and [Config.AfterBulkIndex] to populate
	// related tables (tags, FTS, etc.) that need different structure than the main table.
	//
	// Base columns (id, short_id, path, mtime_ns, title) are included automatically.
	// Add custom columns for fields you want to query or filter on:
	//
	//	SQLSchema: mddb.NewBaseSchema("tickets").
	//	    Text("status", true).    // indexed
	//	    Int("priority", false),  // not indexed
	//
	// Values for custom columns are provided by [Config.SQLColumnValues].
	//
	// Optional. If nil, uses base schema with table name "documents".
	SQLSchema *SQLSchema

	// SQLColumnValues extracts user-defined column values from a document for indexing.
	//
	// Called during [Tx.Commit] and [MDDB.Reindex] to populate custom columns in
	// the SQLite index. Base columns (id, short_id, path, mtime_ns, title) are
	// handled automatically.
	//
	// Return values in the SAME ORDER as columns were defined in SQLSchema:
	//
	//	SQLSchema.Text("status").Int("priority") → return []any{status, priority}
	//
	// All [IndexableDocument] data is borrowed from the file buffer and only valid
	// during the callback.
	//
	// Optional. Required only if SQLSchema has user columns.
	//
	// Example:
	//
	//	SQLSchema: mddb.NewBaseSchema("tickets").
	//	    Text("status", true).
	//	    Int("priority", false),
	//	SQLColumnValues: func(doc mddb.IndexableDocument) []any {
	//	    status, _ := doc.Frontmatter.GetString([]byte("status"))
	//	    priority, _ := doc.Frontmatter.GetInt([]byte("priority"))
	//	    return []any{status, priority}
	//	},
	//
	// Note: frontmatter lookups use []byte keys to avoid allocations. Reuse
	// shared []byte keys in hot paths (for reserved fields, use
	// FrontmatterKeyID/FrontmatterKeySchemaVersion/FrontmatterKeyTitle).
	SQLColumnValues func(doc IndexableDocument) []any

	// LockTimeout is max wait for WAL locks. Default: 10s.
	LockTimeout time.Duration

	// ParseOptions configures frontmatter parsing behavior.
	// Use frontmatter.WithLineLimit, frontmatter.WithRequireDelimiter, etc.
	// Default: no line limit, require opening "---" delimiter.
	ParseOptions []frontmatter.ParseOption

	// RelPathFromID returns the relative file path for this document.
	//
	// Path is relative to [Config.BaseDir]. For example, if BaseDir is
	// "/home/user/data" and RelPathFromID returns "2026/01-28/ABC123.md",
	// the absolute file location is:
	//
	//	/home/user/data/2026/01-28/ABC123.md
	//
	// Optional. Default: flat layout returning "<id>.md".
	//
	// User is responsible for deriving path from ID. mddb validates:
	//   - Path ends with ".md"
	//   - Path does not escape the data directory (no ".." traversal)
	//   - File exists at this path (for reads)
	//   - Actual file location matches RelPathFromID(id) after parsing
	//
	// Example layouts (user's choice):
	//   - "2026/01-28/ABC123.md" (date-based directories)
	//   - "ABC123.md" (flat, all files in root)
	//   - "bugs/ABC123.md" (category-based)
	RelPathFromID func(id string) string

	// ShortIDFromID returns a short identifier derived from the ID.
	//
	// Used by [MDDB.GetByPrefix] which searches both short_id and id columns
	// with prefix matching (LIKE 'prefix%'). This allows human-friendly lookups
	// when IDs are long (e.g., UUIDs).
	//
	// Must be deterministic, stable for each ID, and non-empty.
	//
	// Optional. Default: returns full ID (prefix search works on ID directly).
	//
	// Custom approaches:
	//   - Truncated ID prefix for shorter input
	//   - Base32/crockford encoding for readability
	//   - Hash for uniform length
	ShortIDFromID func(id string) string

	//
	// INDEX LIFECYCLE HOOKS
	// ---------------------
	// These hooks maintain related tables (tags, links, FTS, etc.) in the ephemeral
	// SQLite index. All related tables MUST be rebuildable from markdown files since
	// mddb drops and recreates the entire database on schema changes and reindex.
	//

	// AfterPut updates related tables after a document is written.
	//
	// Called during [Tx.Commit] and mddb WAL replay (crash recovery). Use to sync
	// related tables (tags, blockers, FTS) with the main document table. Must be
	// idempotent since replay may call it multiple times for the same document.
	//
	// NOT called during [MDDB.Reindex] - use [Config.AfterBulkIndex] for that.
	// If you have related tables, implement both callbacks.
	//
	// Optional. Error triggers transaction rollback.
	AfterPut func(ctx context.Context, tx *sql.Tx, doc *T) error

	// AfterDelete cleans up related tables after a document is deleted.
	//
	// Called during [Tx.Commit] and mddb WAL replay (crash recovery). Use to remove
	// entries from related tables. Must be idempotent.
	//
	// Note: the document file is already deleted. If cleanup needs document data
	// (e.g., which tags to remove), query your related table or use ON DELETE CASCADE.
	//
	// Optional. Error triggers transaction rollback.
	AfterDelete func(ctx context.Context, tx *sql.Tx, id string) error

	// AfterRecreateSchema creates related tables after the main table is recreated.
	//
	// Called by [Open] on schema mismatch and [MDDB.Reindex]. Use to DROP and CREATE
	// related tables (tags, FTS, etc.) that will be populated by [Config.AfterBulkIndex].
	// This runs BEFORE any documents are indexed.
	//
	// IMPORTANT: mddb only drops and recreates the MAIN table (defined by [Config.SQLSchema]).
	// Related tables are NOT automatically dropped. You must DROP them yourself here
	// before recreating, otherwise stale data from previous indexes will persist.
	//
	// Example:
	//
	//	AfterRecreateSchema: func(ctx context.Context, tx *sql.Tx) error {
	//	    // Drop related tables first (stale data from previous index)
	//	    _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS tags")
	//	    if err != nil {
	//	        return err
	//	    }
	//	    // Recreate fresh
	//	    _, err = tx.ExecContext(ctx, "CREATE TABLE tags (doc_id TEXT, tag TEXT)")
	//	    return err
	//	}
	//
	// Optional. Error triggers transaction rollback.
	AfterRecreateSchema func(ctx context.Context, tx *sql.Tx) error

	// AfterBulkIndex populates related tables during reindex.
	//
	// Called after each batch of documents is inserted into the main table during
	// [MDDB.Reindex]. Batch size is exactly 50 documents (except final batch).
	// Use to populate related tables (tags, FTS) from the indexed documents.
	//
	// NOT called during [Tx.Commit] - use [Config.AfterPut] for that.
	// If you have related tables, implement both callbacks.
	//
	// Receives [IndexableDocument] (not *T) to avoid parsing overhead and byte
	// copying during bulk operations. Extract values directly from frontmatter.
	//
	// All [IndexableDocument] data is borrowed and valid only during the callback.
	// Passing to sql.Stmt.Exec() is safe (driver copies bytes).
	//
	// Optional. Error triggers transaction rollback.
	AfterBulkIndex func(ctx context.Context, tx *sql.Tx, batch []IndexableDocument) error
}
