// Command mddb is a playground CLI for the mddb package.
//
// Usage:
//
//	mddb create --title "My Doc" [--tags=a,b,c] [--key=value]...
//	mddb list
//	mddb get <id|prefix>
//	mddb delete <id|prefix>
//	mddb query [--key=value]... [--sql "SELECT ..."]
//	mddb reindex
package main

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/calvinalkan/agent-task/pkg/mddb"
	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

const (
	dataDir       = "/tmp/mddb-playground"
	tableName     = "docs"
	shortIDLength = 12
	crockfordBase = "0123456789abcdefghjkmnpqrstvwxyz" // lowercase
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage())
	}

	ctx := context.Background()

	switch args[0] {
	case "create":
		return cmdCreate(ctx, args[1:])
	case "update":
		return cmdUpdate(ctx, args[1:])
	case "list", "ls":
		return cmdList(ctx)
	case "get":
		return cmdGet(ctx, args[1:])
	case "delete", "rm":
		return cmdDelete(ctx, args[1:])
	case "query":
		return cmdQuery(ctx, args[1:])
	case "reindex":
		return cmdReindex(ctx)
	case "seed":
		return cmdSeed(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage())
		return nil
	default:
		return fmt.Errorf("unknown command: %s\n%s", args[0], usage())
	}
}

func usage() string {
	return `mddb playground CLI

Commands:
  create --title "Doc" [--key=value]... [body]   Create document
  update <id> [--key=value]... [body]            Update document
  list, ls                                        List all documents
  get <id|prefix>                                 Get document by ID/prefix
  delete, rm <id|prefix>                          Delete document
  query [--key=value]... [--sql "..."]            Query documents
  reindex                                         Rebuild index from files
  seed <count> [--days=N] [--clean]               Seed N docs for testing

Data: /tmp/mddb-playground

Examples:
  mddb create --title "Buy milk" --tags=groceries,urgent "Don't forget oat milk"
  mddb update abc --priority=low "Updated body"
  mddb list
  mddb get abc
  mddb query --tags=urgent
  mddb query --sql "SELECT * FROM docs WHERE json_extract(extra,'$.priority')='high'"
  mddb seed 10000 --days=30 --clean`
}

// -----------------------------------------------------------------------------
// Document implementation
// -----------------------------------------------------------------------------

// Doc is a simple document with flexible JSON extra fields.
type Doc struct {
	DocID    string         `json:"id"`
	DocShort string         `json:"short_id"`
	DocPath  string         `json:"path"`
	DocMtime int64          `json:"mtime_ns"`
	DocTitle string         `json:"title"`
	DocExtra map[string]any `json:"extra"` // flexible fields
	DocBody  string         `json:"body"`
}

func (d Doc) ID() string      { return d.DocID }
func (d Doc) RelPath() string { return d.DocPath }
func (d Doc) ShortID() string { return d.DocShort }
func (d Doc) MtimeNS() int64  { return d.DocMtime }
func (d Doc) Title() string   { return d.DocTitle }
func (d Doc) Body() string    { return d.DocBody }

func (d Doc) Validate() error {
	if d.DocTitle == "" {
		return errors.New("title is required")
	}
	return nil
}

func (d Doc) Frontmatter() frontmatter.Frontmatter {
	fm := frontmatter.Frontmatter{
		"title": frontmatter.String(d.DocTitle),
	}
	// Render extra fields as normal YAML frontmatter
	for key, val := range d.DocExtra {
		switch v := val.(type) {
		case string:
			fm[key] = frontmatter.String(v)
		case []any:
			strs := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					strs = append(strs, s)
				}
			}
			fm[key] = frontmatter.StringList(strs)
		case []string:
			fm[key] = frontmatter.StringList(v)
		}
	}
	return fm
}

// newDoc creates a Doc with generated UUIDv7 ID.
func newDoc(title string, extra map[string]any, body string) (*Doc, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}

	return &Doc{
		DocID:    id.String(),
		DocShort: shortIDFromUUID(id),
		DocPath:  pathFromID(id),
		DocTitle: title,
		DocExtra: extra,
		DocBody:  body,
	}, nil
}

// shortIDFromUUID extracts 60 bits from UUIDv7 random section, encodes as crockford base32.
func shortIDFromUUID(id uuid.UUID) string {
	randA := (uint16(id[6]&0x0f) << 8) | uint16(id[7])
	randB := (uint64(id[8]&0x3f) << 56) |
		(uint64(id[9]) << 48) |
		(uint64(id[10]) << 40) |
		(uint64(id[11]) << 32) |
		(uint64(id[12]) << 24) |
		(uint64(id[13]) << 16) |
		(uint64(id[14]) << 8) |
		uint64(id[15])
	top60 := (uint64(randA) << 48) | (randB >> 14)

	var buf [shortIDLength]byte
	for i := shortIDLength - 1; i >= 0; i-- {
		buf[i] = crockfordBase[top60&0x1f]
		top60 >>= 5
	}
	return string(buf[:])
}

func pathFromID(id uuid.UUID) string {
	short := shortIDFromUUID(id)
	sec, nsec := id.Time().UnixTime()
	t := time.Unix(sec, nsec).UTC()
	return filepath.Join(t.Format("2006/01-02"), short+".md")
}

// -----------------------------------------------------------------------------
// Config callbacks
// -----------------------------------------------------------------------------

// reservedFields are frontmatter keys managed by mddb or this doc type.
var reservedFields = map[string]bool{
	"id":             true,
	"schema_version": true,
	"title":          true,
}

func parseDoc(idStr string, fm frontmatter.Frontmatter, body string, mtimeNS int64) (*Doc, error) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse id: %w", err)
	}

	title, _ := fm.GetString("title")

	// Collect all non-reserved fields into extra
	extra := make(map[string]any)
	for key, val := range fm {
		if reservedFields[key] {
			continue
		}
		switch val.Kind {
		case frontmatter.ValueScalar:
			switch val.Scalar.Kind {
			case frontmatter.ScalarString:
				extra[key] = val.Scalar.String
			case frontmatter.ScalarInt:
				extra[key] = val.Scalar.Int
			case frontmatter.ScalarBool:
				extra[key] = val.Scalar.Bool
			}
		case frontmatter.ValueList:
			extra[key] = val.List
		}
	}

	return &Doc{
		DocID:    idStr,
		DocShort: shortIDFromUUID(id),
		DocPath:  pathFromID(id),
		DocMtime: mtimeNS,
		DocTitle: title,
		DocExtra: extra,
		DocBody:  body,
	}, nil
}

func recreateIndex(ctx context.Context, tx *sql.Tx, table string) error {
	stmts := []string{
		"DROP TABLE IF EXISTS " + table,
		fmt.Sprintf(`CREATE TABLE %s (
			id        TEXT PRIMARY KEY,
			short_id  TEXT NOT NULL,
			path      TEXT NOT NULL,
			mtime_ns  INTEGER NOT NULL,
			title     TEXT NOT NULL,
			extra     JSON NOT NULL DEFAULT '{}',
			body      TEXT NOT NULL
		) WITHOUT ROWID`, table),
		fmt.Sprintf("CREATE INDEX idx_%s_short_id ON %s(short_id)", table, table),
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

type docStmts struct {
	insert *sql.Stmt
	del    *sql.Stmt
}

func prepareDocStmts(ctx context.Context, tx *sql.Tx, table string) (mddb.PreparedStatements[Doc], error) {
	insert, err := tx.PrepareContext(ctx, fmt.Sprintf(`
		INSERT OR REPLACE INTO %s (id, short_id, path, mtime_ns, title, extra, body)
		VALUES (?, ?, ?, ?, ?, json(?), ?)`, table))
	if err != nil {
		return nil, err
	}

	del, err := tx.PrepareContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", table))
	if err != nil {
		_ = insert.Close()
		return nil, err
	}

	return &docStmts{insert: insert, del: del}, nil
}

func (s *docStmts) Upsert(ctx context.Context, doc *Doc) error {
	extraJSON := "{}"
	if len(doc.DocExtra) > 0 {
		b, _ := json.Marshal(doc.DocExtra)
		extraJSON = string(b)
	}
	_, err := s.insert.ExecContext(ctx,
		doc.DocID, doc.DocShort, doc.DocPath, doc.DocMtime,
		doc.DocTitle, extraJSON, doc.DocBody)
	return err
}

func (s *docStmts) Delete(ctx context.Context, id string) error {
	_, err := s.del.ExecContext(ctx, id)
	return err
}

func (s *docStmts) Close() error {
	return errors.Join(s.insert.Close(), s.del.Close())
}

func openStore(ctx context.Context) (*mddb.MDDB[Doc], error) {
	return mddb.Open(ctx, mddb.Config[Doc]{
		Dir:           dataDir,
		TableName:     tableName,
		SchemaVersion: 1,
		Parse:         parseDoc,
		RecreateIndex: recreateIndex,
		Prepare:       prepareDocStmts,
	})
}

// -----------------------------------------------------------------------------
// Commands
// -----------------------------------------------------------------------------

func cmdCreate(ctx context.Context, args []string) error {
	var title, body string
	extra := make(map[string]any)

	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			// positional arg = body
			body = arg
			continue
		}
		kv := strings.TrimPrefix(arg, "--")
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("invalid flag: %s (use --key=value)", arg)
		}

		if key == "title" {
			title = val
			continue
		}

		// comma = array, else string
		if strings.Contains(val, ",") {
			extra[key] = strings.Split(val, ",")
		} else {
			extra[key] = val
		}
	}

	if title == "" {
		return errors.New("--title is required")
	}

	doc, err := newDoc(title, extra, body)
	if err != nil {
		return err
	}

	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	tx, err := store.Begin(ctx)
	if err != nil {
		return err
	}

	result, err := tx.Put(doc)
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	fmt.Printf("Created: %s (%s)\n", result.ShortID(), result.Title())
	fmt.Printf("ID:      %s\n", result.ID())
	if len(result.DocExtra) > 0 {
		extraJSON, _ := json.MarshalIndent(result.DocExtra, "         ", "  ")
		fmt.Printf("Extra:   %s\n", extraJSON)
	}

	return nil
}

func cmdUpdate(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mddb update <id|prefix> [--key=value]... [body]")
	}

	prefix := args[0]
	args = args[1:]

	var body *string // nil means don't update body
	updates := make(map[string]any)

	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			// positional arg = body
			b := arg
			body = &b
			continue
		}
		kv := strings.TrimPrefix(arg, "--")
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("invalid flag: %s (use --key=value)", arg)
		}

		// comma = array, else string
		if strings.Contains(val, ",") {
			updates[key] = strings.Split(val, ",")
		} else {
			updates[key] = val
		}
	}

	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	// Find document
	matches, err := store.GetByPrefix(ctx, prefix)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no document matching %q", prefix)
	}
	if len(matches) > 1 {
		fmt.Printf("Ambiguous prefix %q, matches:\n", prefix)
		for _, m := range matches {
			fmt.Printf("  %s  %s\n", m.ShortID, m.Title)
		}
		return errors.New("specify more characters")
	}

	// Get full document
	doc, err := store.Get(ctx, matches[0].ID)
	if err != nil {
		return err
	}

	// Apply updates
	for key, val := range updates {
		if key == "title" {
			if s, ok := val.(string); ok {
				doc.DocTitle = s
			}
		} else {
			if doc.DocExtra == nil {
				doc.DocExtra = make(map[string]any)
			}
			doc.DocExtra[key] = val
		}
	}
	if body != nil {
		doc.DocBody = *body
	}

	// Save
	tx, err := store.Begin(ctx)
	if err != nil {
		return err
	}

	result, err := tx.Put(doc)
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	fmt.Printf("Updated: %s (%s)\n", result.ShortID(), result.Title())
	if len(result.DocExtra) > 0 {
		extraJSON, _ := json.MarshalIndent(result.DocExtra, "         ", "  ")
		fmt.Printf("Extra:   %s\n", extraJSON)
	}

	return nil
}

func cmdList(ctx context.Context) error {
	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	docs, err := mddb.Query(ctx, store, func(db *sql.DB) ([]listRow, error) {
		rows, err := db.QueryContext(ctx, "SELECT short_id, title, extra FROM "+tableName+" ORDER BY id")
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var results []listRow
		for rows.Next() {
			var r listRow
			var extraJSON string
			if err := rows.Scan(&r.ShortID, &r.Title, &extraJSON); err != nil {
				return nil, err
			}
			_ = json.Unmarshal([]byte(extraJSON), &r.Extra)
			results = append(results, r)
		}
		return results, rows.Err()
	})
	if err != nil {
		return err
	}

	if len(docs) == 0 {
		fmt.Println("No documents.")
		return nil
	}

	// Table output
	fmt.Printf("%-12s  %-30s  %s\n", "ID", "TITLE", "EXTRA")
	fmt.Printf("%-12s  %-30s  %s\n", "----", "-----", "-----")
	for _, d := range docs {
		extraStr := ""
		if len(d.Extra) > 0 {
			b, _ := json.Marshal(d.Extra)
			extraStr = string(b)
			if len(extraStr) > 40 {
				extraStr = extraStr[:37] + "..."
			}
		}
		title := d.Title
		if len(title) > 30 {
			title = title[:27] + "..."
		}
		fmt.Printf("%-12s  %-30s  %s\n", d.ShortID, title, extraStr)
	}
	fmt.Printf("\n%d document(s)\n", len(docs))

	return nil
}

type listRow struct {
	ShortID string
	Title   string
	Extra   map[string]any
}

func cmdGet(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mddb get <id|prefix>")
	}
	prefix := args[0]

	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	matches, err := store.GetByPrefix(ctx, prefix)
	if err != nil {
		return err
	}

	if len(matches) == 0 {
		return fmt.Errorf("no document matching %q", prefix)
	}
	if len(matches) > 1 {
		fmt.Printf("Ambiguous prefix %q, matches:\n", prefix)
		for _, m := range matches {
			fmt.Printf("  %s  %s\n", m.ShortID, m.Title)
		}
		return nil
	}

	doc, err := store.Get(ctx, matches[0].ID)
	if err != nil {
		return err
	}

	fmt.Printf("ID:       %s\n", doc.ID())
	fmt.Printf("ShortID:  %s\n", doc.ShortID())
	fmt.Printf("Title:    %s\n", doc.Title())
	fmt.Printf("Path:     %s\n", doc.RelPath())
	if len(doc.DocExtra) > 0 {
		extraJSON, _ := json.MarshalIndent(doc.DocExtra, "          ", "  ")
		fmt.Printf("Extra:    %s\n", extraJSON)
	}
	if doc.Body() != "" {
		fmt.Printf("\n---\n%s", doc.Body())
	}

	return nil
}

func cmdDelete(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mddb delete <id|prefix>")
	}
	prefix := args[0]

	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	matches, err := store.GetByPrefix(ctx, prefix)
	if err != nil {
		return err
	}

	if len(matches) == 0 {
		return fmt.Errorf("no document matching %q", prefix)
	}
	if len(matches) > 1 {
		fmt.Printf("Ambiguous prefix %q, matches:\n", prefix)
		for _, m := range matches {
			fmt.Printf("  %s  %s\n", m.ShortID, m.Title)
		}
		return errors.New("specify more characters")
	}

	tx, err := store.Begin(ctx)
	if err != nil {
		return err
	}

	if err := tx.Delete(matches[0].ID, matches[0].Path); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	fmt.Printf("Deleted: %s (%s)\n", matches[0].ShortID, matches[0].Title)
	return nil
}

func cmdQuery(ctx context.Context, args []string) error {
	var rawSQL string
	filters := make(map[string]string)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--sql" {
			if i+1 >= len(args) {
				return errors.New("--sql requires a value")
			}
			rawSQL = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(arg, "--") {
			kv := strings.TrimPrefix(arg, "--")
			key, val, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("invalid flag: %s", arg)
			}
			filters[key] = val
		}
	}

	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	docs, err := mddb.Query(ctx, store, func(db *sql.DB) ([]listRow, error) {
		var query string
		var queryArgs []any

		if rawSQL != "" {
			query = rawSQL
		} else if len(filters) > 0 {
			// Build query from filters
			var conditions []string
			for key, val := range filters {
				// Check if searching in array (json_extract returns array)
				// Use instr for contains check on JSON array
				cond := fmt.Sprintf(
					"(json_extract(extra, '$.%s') = ? OR instr(json_extract(extra, '$.%s'), ?) > 0)",
					key, key)
				conditions = append(conditions, cond)
				queryArgs = append(queryArgs, val, `"`+val+`"`)
			}
			query = "SELECT short_id, title, extra FROM " + tableName +
				" WHERE " + strings.Join(conditions, " AND ") + " ORDER BY id"
		} else {
			query = "SELECT short_id, title, extra FROM " + tableName + " ORDER BY id"
		}

		rows, err := db.QueryContext(ctx, query, queryArgs...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var results []listRow
		for rows.Next() {
			var r listRow
			var extraJSON string
			if err := rows.Scan(&r.ShortID, &r.Title, &extraJSON); err != nil {
				return nil, err
			}
			_ = json.Unmarshal([]byte(extraJSON), &r.Extra)
			results = append(results, r)
		}
		return results, rows.Err()
	})
	if err != nil {
		return err
	}

	if len(docs) == 0 {
		fmt.Println("No results.")
		return nil
	}

	fmt.Printf("%-12s  %-30s  %s\n", "ID", "TITLE", "EXTRA")
	fmt.Printf("%-12s  %-30s  %s\n", "----", "-----", "-----")
	for _, d := range docs {
		extraStr := ""
		if len(d.Extra) > 0 {
			b, _ := json.Marshal(d.Extra)
			extraStr = string(b)
			if len(extraStr) > 40 {
				extraStr = extraStr[:37] + "..."
			}
		}
		title := d.Title
		if len(title) > 30 {
			title = title[:27] + "..."
		}
		fmt.Printf("%-12s  %-30s  %s\n", d.ShortID, title, extraStr)
	}
	fmt.Printf("\n%d result(s)\n", len(docs))

	return nil
}

func cmdReindex(ctx context.Context) error {
	store, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()

	start := time.Now()
	count, err := store.Reindex(ctx)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	// Nice format
	var rate string
	if elapsed > 0 && count > 0 {
		docsPerSec := float64(count) / elapsed.Seconds()
		if docsPerSec >= 1000 {
			rate = fmt.Sprintf(" (%.1fk docs/s)", docsPerSec/1000)
		} else {
			rate = fmt.Sprintf(" (%.0f docs/s)", docsPerSec)
		}
	}

	fmt.Printf("Reindexed %d doc(s) in %s%s\n", count, formatDuration(elapsed), rate)
	return nil
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return d.Round(time.Millisecond).String()
	}
}

func cmdSeed(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mddb seed <count> [--days=N] [--clean]")
	}

	// Parse count
	count, err := strconv.Atoi(args[0])
	if err != nil || count <= 0 {
		return fmt.Errorf("invalid count: %s", args[0])
	}

	// Parse flags
	days := 1
	clean := false
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "--days=") {
			d, err := strconv.Atoi(strings.TrimPrefix(arg, "--days="))
			if err != nil || d <= 0 {
				return fmt.Errorf("invalid --days: %s", arg)
			}
			days = d
		} else if arg == "--clean" {
			clean = true
		}
	}

	if clean {
		fmt.Printf("Cleaning %s...\n", dataDir)
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("clean: %w", err)
		}
	}

	// Ensure data dir exists
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	fmt.Printf("Seeding %d docs across %d day(s)...\n", count, days)

	baseTime := time.Now().UTC()

	// Parallel seeding
	numWorkers := runtime.NumCPU()
	workChan := make(chan int, numWorkers*2)

	var wg sync.WaitGroup
	start := time.Now()

	// Start workers
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range workChan {
				writeSeedDoc(baseTime, i, days)
			}
		}()
	}

	// Send work
	for i := 1; i <= count; i++ {
		workChan <- i
	}
	close(workChan)

	wg.Wait()
	elapsed := time.Since(start)

	var rate string
	if elapsed > 0 {
		filesPerSec := float64(count) / elapsed.Seconds()
		if filesPerSec >= 1000 {
			rate = fmt.Sprintf(" (%.1fk files/s)", filesPerSec/1000)
		} else {
			rate = fmt.Sprintf(" (%.0f files/s)", filesPerSec)
		}
	}

	fmt.Printf("Wrote %d files in %s%s\n", count, formatDuration(elapsed), rate)
	fmt.Println("Run 'mddb reindex' to index.")

	return nil
}

func writeSeedDoc(baseTime time.Time, i int, days int) {
	// Distribute across days by adjusting timestamp
	dayOffset := i % days
	docTime := baseTime.AddDate(0, 0, -dayOffset).Add(time.Duration(i) * time.Millisecond)

	// Generate UUIDv7 with specific timestamp
	id := newUUIDv7WithTime(docTime)
	shortID := shortIDFromUUID(id)
	relPath := pathFromID(id)
	absPath := filepath.Join(dataDir, relPath)

	// Ensure directory exists
	dir := filepath.Dir(absPath)
	_ = os.MkdirAll(dir, 0o750)

	// Vary extra fields
	tags := []string{"seed"}
	if i%3 == 0 {
		tags = append(tags, "important")
	}
	if i%5 == 0 {
		tags = append(tags, "review")
	}

	priority := []string{"low", "medium", "high"}[i%3]
	status := []string{"open", "in_progress", "done"}[i%3]

	content := fmt.Sprintf(`---
id: %s
schema_version: 10001
priority: %s
status: %s
tags:
%stitle: Seed document %d
---

%s
`, id.String(), priority, status, formatTagsYAML(tags), i, seedBody(i))

	_ = os.WriteFile(absPath, []byte(content), 0o600)
	_ = shortID // used in path
}

// seedBody generates a ~100 line body for seed documents.
func seedBody(i int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("# Seed Document %d\n\n", i))
	b.WriteString("This is a test document generated by mddb seed.\n\n")
	b.WriteString("## Description\n\n")
	b.WriteString("Lorem ipsum dolor sit amet, consectetur adipiscing elit.\n")
	b.WriteString("Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.\n\n")
	b.WriteString("## Details\n\n")
	for j := 1; j <= 90; j++ {
		b.WriteString(fmt.Sprintf("Line %d: This is content line number %d for document %d.\n", j, j, i))
	}
	b.WriteString("\n## End\n")
	return b.String()
}

// newUUIDv7WithTime creates a UUIDv7 with a specific timestamp.
func newUUIDv7WithTime(t time.Time) uuid.UUID {
	var id uuid.UUID

	// Unix timestamp in milliseconds (48 bits)
	ms := uint64(t.UnixMilli())
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)

	// Version 7 (4 bits) + random (12 bits)
	id[6] = 0x70 | (byte(ms>>4) & 0x0f) // version 7
	id[7] = byte(ms<<4) | (id[7] & 0x0f)

	// Fill rest with random
	_, _ = crand.Read(id[6:])
	id[6] = (id[6] & 0x0f) | 0x70 // version 7
	id[8] = (id[8] & 0x3f) | 0x80 // variant 2

	return id
}

func formatTagsYAML(tags []string) string {
	var b strings.Builder
	for _, t := range tags {
		b.WriteString("  - ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	return b.String()
}
