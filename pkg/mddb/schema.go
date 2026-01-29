package mddb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// ColumnType represents SQLite storage classes.
type ColumnType uint8

// SQLite column types.
const (
	ColText ColumnType = iota
	ColInt
	ColReal
	ColBlob
)

func (t ColumnType) String() string {
	switch t {
	case ColInt:
		return "INTEGER"
	case ColReal:
		return "REAL"
	case ColBlob:
		return "BLOB"
	default:
		return "TEXT"
	}
}

// columnDef defines a single column in the schema.
type columnDef struct {
	name    string
	typ     ColumnType
	notNull bool
	pk      bool // PRIMARY KEY (only for id)
}

// indexDef defines an index on one or more columns.
type indexDef struct {
	columns []string
	unique  bool
}

// SQLSchema defines the SQLite table structure.
// Pre-populated with base columns; users append or modify.
type SQLSchema struct {
	tableName string
	columns   []columnDef
	indexes   []indexDef
}

// NewBaseSQLSchema creates a schema with the required base columns.
func NewBaseSQLSchema(tableName string) *SQLSchema {
	return &SQLSchema{
		tableName: tableName,
		columns: []columnDef{
			{name: "id", typ: ColText, pk: true},
			{name: "short_id", typ: ColText, notNull: true},
			{name: "path", typ: ColText, notNull: true},
			{name: "mtime_ns", typ: ColInt, notNull: true},
			{name: "title", typ: ColText, notNull: true},
		},
		indexes: []indexDef{
			{columns: []string{"short_id"}, unique: false},
		},
	}
}

// TableName sets a custom table name. Default is "documents".
func (s *SQLSchema) TableName(name string) *SQLSchema {
	s.tableName = name

	return s
}

// Text appends a TEXT column.
func (s *SQLSchema) Text(name string, notNull bool) *SQLSchema {
	s.columns = append(s.columns, columnDef{name: name, typ: ColText, notNull: notNull})

	return s
}

// Int appends an INTEGER column.
func (s *SQLSchema) Int(name string, notNull bool) *SQLSchema {
	s.columns = append(s.columns, columnDef{name: name, typ: ColInt, notNull: notNull})

	return s
}

// Real appends a REAL column.
func (s *SQLSchema) Real(name string, notNull bool) *SQLSchema {
	s.columns = append(s.columns, columnDef{name: name, typ: ColReal, notNull: notNull})

	return s
}

// Blob appends a BLOB column.
func (s *SQLSchema) Blob(name string, notNull bool) *SQLSchema {
	s.columns = append(s.columns, columnDef{name: name, typ: ColBlob, notNull: notNull})

	return s
}

// SetType modifies the type of an existing column (e.g., id from TEXT to INTEGER).
func (s *SQLSchema) SetType(name string, typ ColumnType) *SQLSchema {
	for i := range s.columns {
		if s.columns[i].name == name {
			s.columns[i].typ = typ

			return s
		}
	}

	return s
}

// Index adds an index on the specified columns.
func (s *SQLSchema) Index(columns ...string) *SQLSchema {
	if len(columns) > 0 {
		s.indexes = append(s.indexes, indexDef{columns: columns, unique: false})
	}

	return s
}

// UniqueIndex adds a unique index on the specified columns.
func (s *SQLSchema) UniqueIndex(columns ...string) *SQLSchema {
	if len(columns) > 0 {
		s.indexes = append(s.indexes, indexDef{columns: columns, unique: true})
	}

	return s
}

// SQL returns the CREATE TABLE statement and CREATE INDEX statements.
// Useful for inspection/debugging the generated schema.
func (s *SQLSchema) SQL() (string, []string) {
	return s.buildCreateTableSQL(), s.buildCreateIndexSQL()
}

// columnNames returns all column names in order.
func (s *SQLSchema) columnNames() []string {
	names := make([]string, len(s.columns))
	for i, col := range s.columns {
		names[i] = col.name
	}

	return names
}

// userColumnCount returns the number of user-defined columns (after base columns).
func (s *SQLSchema) userColumnCount() int {
	if len(s.columns) <= 5 {
		return 0
	}

	return len(s.columns) - 5
}

// validate ensures base columns still exist, no duplicates, and valid identifiers.
func (s *SQLSchema) validate() error {
	if s.tableName == "" {
		return errors.New("schema: table name is required")
	}

	if !isValidIdentifier(s.tableName) {
		return fmt.Errorf("schema: invalid table name %q: must be lowercase a-z and underscore", s.tableName)
	}

	required := map[string]bool{
		"id":       false,
		"short_id": false,
		"path":     false,
		"mtime_ns": false,
		"title":    false,
	}

	seen := make(map[string]struct{}, len(s.columns))

	for _, col := range s.columns {
		if !isValidIdentifier(col.name) {
			return fmt.Errorf("schema: invalid column name %q: must be lowercase a-z and underscore", col.name)
		}

		if _, ok := seen[col.name]; ok {
			return fmt.Errorf("schema: duplicate column %q", col.name)
		}

		seen[col.name] = struct{}{}

		if _, ok := required[col.name]; ok {
			required[col.name] = true
		}
	}

	for name, found := range required {
		if !found {
			return fmt.Errorf("schema: missing required column %q", name)
		}
	}

	// Validate indexes reference existing columns
	for _, idx := range s.indexes {
		for _, col := range idx.columns {
			if _, ok := seen[col]; !ok {
				return fmt.Errorf("schema: index references unknown column %q", col)
			}
		}
	}

	return nil
}

// fingerprint computes a hash of the schema structure for version detection.
// Order-independent: columns and indexes are sorted by name before hashing.
// Used to detect schema changes that require reindexing.
func (s *SQLSchema) fingerprint() uint32 {
	h := fnv.New32a()

	// fnv Write never returns an error, but we explicitly ignore for lint.
	_, _ = h.Write([]byte(s.tableName))

	// Sort columns by name for order independence
	sortedCols := make([]columnDef, len(s.columns))
	copy(sortedCols, s.columns)
	sort.Slice(sortedCols, func(i, j int) bool {
		return sortedCols[i].name < sortedCols[j].name
	})

	for _, col := range sortedCols {
		_, _ = h.Write([]byte(col.name))
		_, _ = h.Write([]byte{byte(col.typ)})

		if col.notNull {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}

		if col.pk {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	}

	// Sort indexes by canonical form (joined column names + unique flag)
	sortedIdxs := make([]indexDef, len(s.indexes))
	copy(sortedIdxs, s.indexes)
	sort.Slice(sortedIdxs, func(i, j int) bool {
		iKey := strings.Join(sortedIdxs[i].columns, ",")

		jKey := strings.Join(sortedIdxs[j].columns, ",")
		if iKey != jKey {
			return iKey < jKey
		}

		return sortedIdxs[i].unique && !sortedIdxs[j].unique
	})

	for _, idx := range sortedIdxs {
		for _, col := range idx.columns {
			_, _ = h.Write([]byte(col))
		}

		if idx.unique {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	}

	return h.Sum32()
}

// buildCreateTableSQL generates the CREATE TABLE statement.
func (s *SQLSchema) buildCreateTableSQL() string {
	var b strings.Builder

	b.WriteString("CREATE TABLE ")
	b.WriteString(s.tableName)
	b.WriteString(" (\n")

	for i, col := range s.columns {
		if i > 0 {
			b.WriteString(",\n")
		}

		b.WriteString("    ")
		b.WriteString(col.name)
		b.WriteString(" ")
		b.WriteString(col.typ.String())

		if col.pk {
			b.WriteString(" PRIMARY KEY")
		}

		if col.notNull && !col.pk {
			b.WriteString(" NOT NULL")
		}
	}

	b.WriteString("\n) WITHOUT ROWID")

	return b.String()
}

// buildCreateIndexSQL generates CREATE INDEX statements.
func (s *SQLSchema) buildCreateIndexSQL() []string {
	if len(s.indexes) == 0 {
		return nil
	}

	tableName := s.tableName
	stmts := make([]string, 0, len(s.indexes))

	for i, idx := range s.indexes {
		var b strings.Builder

		if idx.unique {
			b.WriteString("CREATE UNIQUE INDEX ")
		} else {
			b.WriteString("CREATE INDEX ")
		}

		// Generate index name: idx_{table}_{col1}_{col2}...
		b.WriteString("idx_")
		b.WriteString(tableName)
		b.WriteString("_")
		b.WriteString(strings.Join(idx.columns, "_"))

		// Handle potential name collisions with a suffix
		if i > 0 {
			for j := range i {
				if strings.Join(s.indexes[j].columns, "_") == strings.Join(idx.columns, "_") {
					fmt.Fprintf(&b, "_%d", i)

					break
				}
			}
		}

		b.WriteString(" ON ")
		b.WriteString(tableName)
		b.WriteString(" (")
		b.WriteString(strings.Join(idx.columns, ", "))
		b.WriteString(")")

		stmts = append(stmts, b.String())
	}

	return stmts
}

// buildDropTableSQL generates the DROP TABLE IF EXISTS statement.
func (s *SQLSchema) buildDropTableSQL() string {
	return "DROP TABLE IF EXISTS " + s.tableName
}

// recreate drops and recreates the table and indexes within the given transaction.
func (s *SQLSchema) recreate(ctx context.Context, tx *sql.Tx) error {
	// Drop existing table
	if _, err := tx.ExecContext(ctx, s.buildDropTableSQL()); err != nil {
		return fmt.Errorf("sqlite: drop table: %w", err)
	}

	// Create table
	if _, err := tx.ExecContext(ctx, s.buildCreateTableSQL()); err != nil {
		return fmt.Errorf("sqlite: create table: %w", err)
	}

	// Create indexes
	for _, stmt := range s.buildCreateIndexSQL() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite: create index: %w", err)
		}
	}

	return nil
}

// isValidIdentifier checks if s is a valid SQL identifier (a-z, underscore only).
func isValidIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}

	return true
}

// prepareUpsertStmt prepares an INSERT OR REPLACE statement for bulk inserts.
// The statement accepts (rows * columnCount) placeholders, allowing multiple
// documents to be inserted in a single exec call for better performance.
func (mddb *MDDB[T]) prepareUpsertStmt(ctx context.Context, tx *sql.Tx, rows int) (*sql.Stmt, error) {
	sqlStr := _buildUpsertSQL(mddb.schema.tableName, mddb.schema.columnNames(), rows)

	stmt, err := tx.PrepareContext(ctx, sqlStr)
	if err != nil {
		return nil, fmt.Errorf("sqlite: prepare upsert statement: %w", err)
	}

	return stmt, nil
}

// fillBatchUpsertSQLArgs populates dest with SQL arguments for a batch of documents.
// Each document fills colCount consecutive slots in dest.
// Caller must ensure len(dest) >= len(docs) * colCount.
func (mddb *MDDB[T]) fillBatchUpsertSQLArgs(docs []IndexableDocument, colCount int, dest []any) error {
	for i := range docs {
		docArgs := dest[i*colCount : (i+1)*colCount]

		err := mddb._fillDocUpsertSQLArgs(&docs[i], docArgs)
		if err != nil {
			return err
		}
	}

	return nil
}

// _fillDocUpsertSQLArgs populates dest with SQL arguments for a single document.
// First 5 slots are base columns (id, short_id, path, mtime_ns, title),
// remaining slots are filled by Config.SQLColumnValues for user columns.
func (mddb *MDDB[T]) _fillDocUpsertSQLArgs(doc *IndexableDocument, dest []any) error {
	dest[0] = string(doc.ID)
	dest[1] = string(doc.ShortID)
	dest[2] = string(doc.RelPath)
	dest[3] = doc.MtimeNS
	dest[4] = string(doc.Title)

	userColCount := mddb.schema.userColumnCount()
	if userColCount == 0 {
		return nil
	}

	if mddb.cfg.SQLColumnValues == nil {
		return errors.New("internal error: cfg.SQLColumnValues is nil")
	}

	userVals := mddb.cfg.SQLColumnValues(*doc)
	if len(userVals) != userColCount {
		return fmt.Errorf("column values: expected %d values, got %d", userColCount, len(userVals))
	}

	copy(dest[5:], userVals)

	return nil
}

// buildUpsertSQL generates an INSERT OR REPLACE statement with multiple value rows.
// Example for 2 rows, 3 columns: INSERT OR REPLACE INTO t (a,b,c) VALUES (?,?,?), (?,?,?)
func _buildUpsertSQL(table string, columns []string, rows int) string {
	var b strings.Builder

	b.WriteString("INSERT OR REPLACE INTO ")
	b.WriteString(table)
	b.WriteString(" (")

	for i, col := range columns {
		if i > 0 {
			b.WriteString(", ")
		}

		b.WriteString(col)
	}

	b.WriteString(") VALUES ")

	rowPlaceholder := "(" + strings.TrimRight(strings.Repeat("?,", len(columns)), ",") + ")"

	for i := range rows {
		if i > 0 {
			b.WriteString(", ")
		}

		b.WriteString(rowPlaceholder)
	}

	return b.String()
}
