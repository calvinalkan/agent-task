package mddb_test

import (
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Schema_Creates_Base_Table_SQL_When_Called(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("documents")
	tableSQL, indexSQL := s.SQL()

	expected := []string{
		"CREATE TABLE documents",
		"id TEXT PRIMARY KEY",
		"short_id TEXT NOT NULL",
		"path TEXT NOT NULL",
		"mtime_ns INTEGER NOT NULL",
		"size_bytes INTEGER NOT NULL",
		"title TEXT NOT NULL",
		"WITHOUT ROWID",
	}

	for _, want := range expected {
		if !strings.Contains(tableSQL, want) {
			t.Errorf("missing %q in:\n%s", want, tableSQL)
		}
	}

	// Base schema includes short_id index
	if len(indexSQL) != 1 {
		t.Fatalf("got %d index statements, want 1", len(indexSQL))
	}

	if !strings.Contains(indexSQL[0], "idx_documents_short_id") {
		t.Errorf("missing short_id index: %s", indexSQL[0])
	}
}

func Test_Schema_Appends_User_Columns_When_Builder_Called(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("documents").
		Text("status", true).
		Int("priority", false).
		Real("score", true).
		Blob("data", false)

	tableSQL, _ := s.SQL()

	expected := []string{
		"status TEXT NOT NULL",
		"priority INTEGER",
		"score REAL NOT NULL",
		"data BLOB",
	}

	for _, want := range expected {
		if !strings.Contains(tableSQL, want) {
			t.Errorf("missing %q in:\n%s", want, tableSQL)
		}
	}

	// priority should NOT have NOT NULL
	if strings.Contains(tableSQL, "priority INTEGER NOT NULL") {
		t.Errorf("priority should be nullable:\n%s", tableSQL)
	}
}

func Test_Schema_Changes_Column_Type_When_SetType_Called(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("documents").SetType("id", mddb.ColInt)
	tableSQL, _ := s.SQL()

	if !strings.Contains(tableSQL, "id INTEGER PRIMARY KEY") {
		t.Errorf("expected id to be INTEGER:\n%s", tableSQL)
	}
}

func Test_Schema_Creates_Index_SQL_When_Index_Called(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("documents").
		Text("status", true).
		Index("status")

	_, indexSQL := s.SQL()

	// Base schema has short_id index + user status index
	if len(indexSQL) != 2 {
		t.Fatalf("got %d index statements, want 2", len(indexSQL))
	}

	if !strings.Contains(indexSQL[1], "CREATE INDEX") || !strings.Contains(indexSQL[1], "status") {
		t.Errorf("unexpected status index: %s", indexSQL[1])
	}
}

func Test_Schema_Creates_Unique_Index_SQL_When_UniqueIndex_Called(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("documents").
		Text("status", true).
		Int("priority", true).
		UniqueIndex("status", "priority")

	_, indexSQL := s.SQL()

	// Base schema has short_id index + user unique index
	if len(indexSQL) != 2 {
		t.Fatalf("got %d index statements, want 2", len(indexSQL))
	}

	if !strings.Contains(indexSQL[1], "CREATE UNIQUE INDEX") {
		t.Errorf("expected UNIQUE index: %s", indexSQL[1])
	}

	if !strings.Contains(indexSQL[1], "status, priority") {
		t.Errorf("expected compound index columns: %s", indexSQL[1])
	}
}

func Test_Schema_Uses_Custom_Table_Name_When_Set(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("tickets")
	tableSQL, indexSQL := s.SQL()

	if !strings.Contains(tableSQL, "CREATE TABLE tickets") {
		t.Errorf("expected custom table name in SQL:\n%s", tableSQL)
	}

	if len(indexSQL) == 0 || !strings.Contains(indexSQL[0], "idx_tickets_short_id") {
		t.Errorf("expected custom table name in index:\n%v", indexSQL)
	}
}

func Test_Schema_Allows_TableName_Override_When_Called(t *testing.T) {
	t.Parallel()

	s := mddb.NewBaseSQLSchema("documents").TableName("tickets")
	tableSQL, _ := s.SQL()

	if !strings.Contains(tableSQL, "CREATE TABLE tickets") {
		t.Errorf("expected overridden table name:\n%s", tableSQL)
	}
}
