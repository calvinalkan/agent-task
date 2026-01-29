# MDDB API Refactor

This document outlines the API changes to simplify the mddb interface and enable future performance optimizations (batched inserts, arena allocation).

## Goals

1. Simplify user-facing API - one function for column extraction instead of `PreparedStatements` interface
2. Enable batched SQL inserts (50 rows per INSERT) internally
3. Delete by id only + deterministic `RelPathFromID`/`ShortIDFromID` (ErrNotFound on missing file)
4. WAL-safe callbacks for related tables (AfterPut/AfterDelete/AfterBulkIndex)

## Type Changes

### New Types

```go
// Column represents a single column name/value pair for SQL insertion.
type Column struct {
    Name  string
    Value any
}

// ScannedDoc holds parsed document data during bulk reindex.
//
// IMPORTANT: All data is borrowed and only valid during the AfterBulkIndex callback.
// Do not retain any fields after the callback returns.
//
// Borrowed fields:
//   - ID, ShortID, Path, Title, Body: byte slices pointing into internal buffers
//   - FM: Frontmatter with borrowed string values (keys and values are not copied)
//
// Safe operations during callback:
//   - Pass to sql.Stmt.Exec() - driver copies the bytes
//   - Read values for computation
//   - Copy explicitly if needed: copied := append([]byte(nil), doc.ID...)
//
// Unsafe after callback returns:
//   - Storing slices in long-lived data structures
//   - Returning slices from the callback
type ScannedDoc struct {
    ID      []byte
    ShortID []byte
    Path    []byte
    MtimeNS int64
    Title   []byte
    Body    []byte
    FM      frontmatter.Frontmatter  // borrowed - values point into internal buffers
}
```

### Removed Types

```go
// DELETE - no longer needed
type PreparedStatements[T Document] interface {
    Upsert(ctx context.Context, doc *T) error
    Delete(ctx context.Context, id string) error
    Close() error
}
```

### Config Changes

```go
type Config[T Document] struct {
    Dir                  string
    TableName            string
    LockTimeout          time.Duration
    SchemaVersion        int
    FrontmatterLineLimit int

    // KEEP - unchanged
    Parse         func(id string, fm frontmatter.Frontmatter, body string, mtimeNS int64) (*T, error)
    RecreateIndex func(ctx context.Context, tx *sql.Tx, tableName string) error

    // ADD - deterministic path derivation (required)
    // Used by Tx.Delete and validation. Must be stable for each ID.
    RelPathFromID func(id string) string

    // ADD - deterministic short id derivation (required)
    // Used for prefix search and indexing. Must be stable + non-empty.
    ShortIDFromID func(id string) string

    // DELETE
    // Prepare func(ctx context.Context, tx *sql.Tx, tableName string) (PreparedStatements[T], error)

    // ADD - replaces Prepare/PreparedStatements
    // Extracts user-defined columns for SQL indexing.
    // Base columns (id, short_id, path, mtime_ns, title) are handled automatically.
    // Called for each document during index operations.
    ExtractColumns func(fm frontmatter.Frontmatter, body []byte) []Column

    // ADD - called after each Put (normal commit and WAL replay)
    // Use for related tables (blockers, tags, etc.). Must be idempotent.
    // Optional - nil means no post-processing, error => transaction rollback.
    AfterPut func(ctx context.Context, tx *sql.Tx, doc *T) error

    // ADD - called after each Delete (normal commit and WAL replay)
    // Use for related tables. Must be idempotent.
    // Optional - nil means no post-processing, error => transaction rollback.
    AfterDelete func(ctx context.Context, tx *sql.Tx, id string) error

    // ADD - called after each batch of main table inserts during Reindex.
    // Batch size matches internal insert batching (currently 50 docs).
    // Use for related tables. Optional - nil means no post-processing.
    // Error triggers transaction rollback.
    //
    // All ScannedDoc data is valid for the duration of the reindex operation.
    // The sql.Stmt.Exec() driver copies bytes, so passing to SQL is safe.
    AfterBulkIndex func(ctx context.Context, tx *sql.Tx, batch []ScannedDoc) error
}
```

### Tx.Delete Signature Change

```go
// BEFORE
func (tx *Tx[T]) Delete(id string, path string) error

// AFTER - id only (path derived from RelPathFromID)
// Returns ErrNotFound if file does not exist.
func (tx *Tx[T]) Delete(id string) error
```

## Internal Changes

### WAL Operations

Delete ops store only `id` + `path` (no content). Path is derived via `RelPathFromID`.

### SQL Insert Batching during reindex

Replace single-row inserts with batched multi-row VALUES:

```go
// BEFORE - one insert per doc
for _, doc := range docs {
    stmt.Exec(doc.ID, doc.ShortID, ...)
}

// AFTER - batch 50 rows per insert
const batchSize = 50

// Build: INSERT INTO docs (id, short_id, ...) VALUES (?,?,...), (?,?,...), ...
batchSQL := buildBatchInsert(tableName, columns, batchSize)
batchStmt, _ := tx.Prepare(batchSQL)

args := make([]any, batchSize * len(columns))
for i := 0; i < len(docs); i += batchSize {
    batch := docs[i:min(i+batchSize, len(docs))]
    
    if len(batch) == batchSize {
        // Full batch
        fillArgs(args, batch, extractColumns)
        batchStmt.Exec(args...)
    } else {
        // Partial batch - build smaller batch statement, reuse args buffer
        remainderSQL := buildBatchInsert(tableName, columns, len(batch))
        remainderStmt, _ := tx.Prepare(remainderSQL)
        remainderArgs := args[:len(batch)*len(columns)]
        fillArgs(remainderArgs, batch, extractColumns)
        remainderStmt.Exec(remainderArgs...)
        remainderStmt.Close()
    }
}
```

### Base Columns

We automatically handle these columns:

| Column | Source |
|--------|--------|
| `id` | `doc.ID()` or WAL op ID |
| `short_id` | `cfg.ShortIDFromID(id)` |
| `path` | `cfg.RelPathFromID(id)` or WAL op Path |
| `mtime_ns` | file stat |
| `title` | `doc.Title()` |

User's `ExtractColumns` only returns additional columns (status, priority, body, etc.).

### updateSqliteIndexFromOps Changes

```go
func (mddb *MDDB[T]) updateSqliteIndexFromOps(ctx context.Context, ops []walOp[T]) error {
    tx, _ := mddb.sql.BeginTx(ctx, nil)
    
    // Build base + user columns
    baseColumns := []string{"id", "short_id", "path", "mtime_ns", "title"}
    
    // Get user columns from first doc (assumes consistent schema)
    // Or require ExtractColumns to be deterministic
    
    // Prepare batched statement
    batchStmt := prepareBatchInsert(tx, mddb.tableName, allColumns, 50)
    singleStmt := prepareSingleInsert(tx, mddb.tableName, allColumns)
    deleteStmt := prepareDelete(tx, mddb.tableName)
    
    // Process ops with batching
    var putOps []walOp[T]
    for _, op := range ops {
        switch op.Op {
        case walOpPut:
            putOps = append(putOps, op)
        case walOpDelete:
            deleteStmt.Exec(op.ID)
            if mddb.cfg.AfterDelete != nil {
                mddb.cfg.AfterDelete(ctx, tx, op.ID)
            }
        }
    }
    
    // Batched inserts for puts
    executeBatchedInserts(putOps, batchStmt, singleStmt, mddb.cfg.ExtractColumns)
    
    // AfterPut callbacks
    if mddb.cfg.AfterPut != nil {
        for _, op := range putOps {
            doc := parseDoc(op.Content)
            mddb.cfg.AfterPut(ctx, tx, doc)
        }
    }
    
    tx.Commit()
}
```

### reindexSQLInTxn Changes

```go
func (mddb *MDDB[T]) reindexSQLInTxn(ctx context.Context, entries []fileproc.Result[T]) (int, error) {
    tx, _ := mddb.sql.BeginTx(ctx, nil)
    
    // RecreateIndex - unchanged
    mddb.cfg.RecreateIndex(ctx, tx, mddb.tableName)
    
    // Batched inserts (50 rows per INSERT)
    batchStmt := prepareBatchInsert(tx, mddb.tableName, allColumns, 50)
    singleStmt := prepareSingleInsert(tx, mddb.tableName, allColumns)
    
    executeBatchedInserts(entries, batchStmt, singleStmt, mddb.cfg.ExtractColumns)
    
    // Batched inserts + AfterBulkIndex callback per batch
    for i := 0; i < len(entries); i += batchSize {
        batch := entries[i:min(i+batchSize, len(entries))]
        
        // Main table batch insert
        executeBatchInsert(batch, batchStmt, singleStmt, mddb.cfg.ExtractColumns)
        
        // Related tables callback (same batch)
        if mddb.cfg.AfterBulkIndex != nil {
            scannedBatch := convertToScannedDocs(batch)
            mddb.cfg.AfterBulkIndex(ctx, tx, scannedBatch)
        }
    }
    
    tx.Commit()
}
```

## User Migration

### Before

```go
// User had to implement PreparedStatements interface
type ticketStmts struct {
    insert *sql.Stmt
    del    *sql.Stmt
}

func prepareTicketStmts(ctx context.Context, tx *sql.Tx, table string) (mddb.PreparedStatements[Ticket], error) {
    insert, _ := tx.PrepareContext(ctx, fmt.Sprintf(`
        INSERT OR REPLACE INTO %s (id, short_id, path, mtime_ns, title, status, priority, body)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, table))
    del, _ := tx.PrepareContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", table))
    return &ticketStmts{insert: insert, del: del}, nil
}

func (s *ticketStmts) Upsert(ctx context.Context, doc *Ticket) error {
    _, err := s.insert.ExecContext(ctx,
        doc.ID, doc.ShortID, doc.Path, doc.MtimeNS,
        doc.Title, doc.Status, doc.Priority, doc.Body)
    return err
}

func (s *ticketStmts) Delete(ctx context.Context, id string) error {
    _, err := s.del.ExecContext(ctx, id)
    return err
}

func (s *ticketStmts) Close() error {
    return errors.Join(s.insert.Close(), s.del.Close())
}

// Config
cfg := mddb.Config[Ticket]{
    Prepare: prepareTicketStmts,
    // ...
}

// Delete call
tx.Delete(ticket.ID, ticket.Path)
```

### After

```go
// Just a function - no interface to implement
cfg := mddb.Config[Ticket]{
    RelPathFromID: relPathFromID,
    ShortIDFromID: shortIDFromID,
    ExtractColumns: func(fm frontmatter.Frontmatter, body []byte) []mddb.Column {
        status, _ := fm.GetString([]byte("status"))
        priority, _ := fm.GetInt([]byte("priority"))
        return []mddb.Column{
            {"status", status},
            {"priority", priority},
            {"body", body},
        }
    },
    
    // Optional: for related tables
    AfterPut: func(ctx context.Context, tx *sql.Tx, doc *Ticket) error {
        // Delete + re-insert for idempotency
        tx.ExecContext(ctx, "DELETE FROM blockers WHERE ticket_id = ?", doc.ID)
        for _, b := range doc.Blockers {
            tx.ExecContext(ctx, "INSERT INTO blockers (ticket_id, blocker_id) VALUES (?, ?)", 
                doc.ID, b)
        }
        return nil
    },
    
    AfterDelete: func(ctx context.Context, tx *sql.Tx, id string) error {
        tx.ExecContext(ctx, "DELETE FROM blockers WHERE ticket_id = ?", id)
        return nil
    },
    // ...
}

// Delete call - id only
tx.Delete(ticket.ID)
```

## Future: Arena Allocation

This refactor prepares for future arena-allocated reads from fileproc:

1. `ExtractColumns` receives `[]byte` (can be borrowed from arena)
2. `ScannedDoc` uses `[]byte` fields (can point into arena)
3. SQLite driver copies bytes during `Exec`, so borrowed data is safe
4. No `*T` allocation needed during bulk reindex hot path

When fileproc adds arena support:
- `LazyFile.Bytes()` returns arena-allocated slice
- Pre-sized from `Stat.Size` (no realloc)
- All data valid until `ProcessStat` returns
- We insert into SQLite before returning → safe

## Files to Modify

1. `pkg/mddb/types.go` - Config, new types, remove PreparedStatements
2. `pkg/mddb/tx.go` - Delete(id), RelPathFromID checks, ErrNotFound
3. `pkg/mddb/wal.go` - Delete ops id-only, AfterDelete(id)
4. `pkg/mddb/reindex.go` - Batched inserts, AfterBulkIndex callback
5. `pkg/mddb/mddb.go` - Validation for new Config fields
6. `pkg/mddb/parse.go` - Validate RelPathFromID derivation
7. `pkg/mddb/testing_test.go` - Update test config
8. `cmd/mddb/main.go` - Update playground config
9. `pkg/mddb/index_sql.go` - Batch insert helpers

## Testing

1. Verify batched inserts produce same results as single inserts
2. WAL replay with AfterPut/AfterDelete callbacks
3. Delete returns ErrNotFound when file missing
4. AfterDelete called with id only
5. AfterBulkIndex called during Reindex

## IMPORTANT: We do NOT need backwards compat! Clean cut, its unreleased software.
