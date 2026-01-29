package mddb_test

import (
	"database/sql"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Open_RebuildsIndex_When_UserVersionMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Bootstrap")
	writeTestDocFile(t, dir, doc)

	s := openTestStore(t, dir)
	t.Cleanup(func() { _ = s.Close() })

	db := openIndex(t, dir)
	t.Cleanup(func() { _ = db.Close() })

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	// Combined version = internal(1) * 10000 + user(1) = 10001
	if version != 10001 {
		t.Fatalf("user_version = %d, want 10001", version)
	}

	if count := countDocs(t, db); count != 1 {
		t.Fatalf("doc count = %d, want 1", count)
	}
}

func Test_GetByPrefix_Returns_Single_Doc_When_ShortID_Matches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := putTestDoc(t.Context(), t, s, newTestDoc(t, "Test Doc"))

	// Use first 4 chars of short_id as prefix
	prefix := doc.DocShort[:4]

	results, err := s.GetByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}

	if results[0].ID != doc.DocID {
		t.Fatalf("id = %s, want %s", results[0].ID, doc.DocID)
	}

	if results[0].ShortID != doc.DocShort {
		t.Fatalf("short_id = %s, want %s", results[0].ShortID, doc.DocShort)
	}
}

func Test_GetByPrefix_Returns_Single_Doc_When_ID_Prefix_Matches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := putTestDoc(t.Context(), t, s, newTestDoc(t, "Test Doc"))

	// Use first 8 chars of ID as prefix
	prefix := doc.DocID[:8]

	results, err := s.GetByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}

	if results[0].ID != doc.DocID {
		t.Fatalf("id = %s, want %s", results[0].ID, doc.DocID)
	}
}

func Test_GetByPrefix_Returns_Empty_When_No_Match(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	putTestDoc(t.Context(), t, s, newTestDoc(t, "Test Doc"))

	results, err := s.GetByPrefix(t.Context(), "ZZZZZZ")
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("results = %d, want 0", len(results))
	}
}

func Test_GetByPrefix_Returns_Multiple_Docs_When_Prefix_Ambiguous(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc1 := putTestDoc(t.Context(), t, s, newTestDoc(t, "Doc One"))
	doc2 := putTestDoc(t.Context(), t, s, newTestDoc(t, "Doc Two"))

	// Both IDs share the same prefix (first 8 chars)
	prefix := doc1.DocID[:8]

	results, err := s.GetByPrefix(t.Context(), prefix)
	if err != nil {
		t.Fatalf("get by prefix: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}

	ids := map[string]bool{results[0].ID: true, results[1].ID: true}
	if !ids[doc1.DocID] || !ids[doc2.DocID] {
		t.Fatalf("expected both docs, got %v", ids)
	}
}

func Test_GetByPrefix_Returns_Error_When_Prefix_Empty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	_, err := s.GetByPrefix(t.Context(), "")
	if err == nil {
		t.Fatal("expected error for empty prefix")
	}
}

func Test_Query_Returns_All_Docs_When_No_Filter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	putTestDoc(t.Context(), t, s, newTestDoc(t, "Doc A"))
	putTestDoc(t.Context(), t, s, newTestDoc(t, "Doc B"))
	putTestDoc(t.Context(), t, s, newTestDoc(t, "Doc C"))

	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		err := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, err
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func Test_Query_Filters_Results_When_Status_Specified(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	docOpen := newTestDoc(t, "Open Doc")
	docOpen.DocStatus = "open"
	putTestDoc(t.Context(), t, s, docOpen)

	docClosed := newTestDoc(t, "Closed Doc")
	docClosed.DocStatus = "closed"
	putTestDoc(t.Context(), t, s, docClosed)

	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		err := db.QueryRow("SELECT COUNT(*) FROM "+testTableName+" WHERE status = ?", "open").Scan(&n)

		return n, err
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 1 {
		t.Fatalf("open count = %d, want 1", count)
	}
}

func Test_Query_Filters_Results_When_Priority_Specified(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	docP1 := newTestDoc(t, "Priority 1")
	docP1.DocPriority = 1
	putTestDoc(t.Context(), t, s, docP1)

	docP2 := newTestDoc(t, "Priority 2")
	docP2.DocPriority = 2
	putTestDoc(t.Context(), t, s, docP2)

	docP3 := newTestDoc(t, "Priority 3")
	docP3.DocPriority = 3
	putTestDoc(t.Context(), t, s, docP3)

	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		err := db.QueryRow("SELECT COUNT(*) FROM "+testTableName+" WHERE priority >= ?", 2).Scan(&n)

		return n, err
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 2 {
		t.Fatalf("priority >= 2 count = %d, want 2", count)
	}
}

func Test_Query_Limits_Results_When_Limit_Specified(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	for i := range 5 {
		putTestDoc(t.Context(), t, s, newTestDoc(t, "Doc "+string(rune('A'+i))))
	}

	titles, err := mddb.Query(t.Context(), s, func(db *sql.DB) ([]string, error) {
		rows, err := db.Query("SELECT title FROM " + testTableName + " ORDER BY id LIMIT 3")
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var result []string

		for rows.Next() {
			var title string

			err := rows.Scan(&title)
			if err != nil {
				return nil, err
			}

			result = append(result, title)
		}

		return result, rows.Err()
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(titles) != 3 {
		t.Fatalf("titles = %d, want 3", len(titles))
	}
}

func Test_Query_Returns_Typed_Results_When_Custom_Struct_Used(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Structured Query")
	doc.DocStatus = "in_progress"
	doc.DocPriority = 5
	putTestDoc(t.Context(), t, s, doc)

	type result struct {
		ID       string
		Title    string
		Status   string
		Priority int64
	}

	results, err := mddb.Query(t.Context(), s, func(db *sql.DB) ([]result, error) {
		rows, err := db.Query("SELECT id, title, status, priority FROM " + testTableName)
		if err != nil {
			return nil, err
		}
		defer rows.Close()

		var out []result

		for rows.Next() {
			var r result

			err := rows.Scan(&r.ID, &r.Title, &r.Status, &r.Priority)
			if err != nil {
				return nil, err
			}

			out = append(out, r)
		}

		return out, rows.Err()
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}

	if results[0].Title != "Structured Query" {
		t.Fatalf("title = %s, want Structured Query", results[0].Title)
	}

	if results[0].Status != "in_progress" {
		t.Fatalf("status = %s, want in_progress", results[0].Status)
	}

	if results[0].Priority != 5 {
		t.Fatalf("priority = %d, want 5", results[0].Priority)
	}
}
