package sqlguard

import (
	"errors"
	"reflect"
	"testing"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

func TestClassifySQL(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want domain.SQLClass
	}{
		{"plain select", "SELECT * FROM users;", domain.SQLRead},
		{"leading comments select", "-- fetch\n/* multi\nline */ SELECT 1", domain.SQLRead},
		{"pragma read", "PRAGMA table_info('users')", domain.SQLRead},
		{"pragma write", "PRAGMA query_only = OFF", domain.SQLWrite},
		{"cte select", "WITH active AS (SELECT id FROM users) SELECT * FROM active", domain.SQLRead},
		{"cte delete", "WITH old AS (SELECT id FROM users) DELETE FROM users WHERE id IN (SELECT id FROM old)", domain.SQLWrite},
		{"chained read and drop", "SELECT 1; DROP TABLE users;", domain.SQLDDL},
		{"semicolon inside string literal", "SELECT 'a; DROP TABLE users' FROM users", domain.SQLRead},
		{"update", "UPDATE users SET status = 'active' WHERE id = 1", domain.SQLWrite},
		{"explain query plan", "EXPLAIN QUERY PLAN SELECT * FROM users", domain.SQLRead},
		{"explain analyze delete", "EXPLAIN ANALYZE DELETE FROM users WHERE id = 1", domain.SQLWrite},
		{"create table", "CREATE TABLE t (id INT)", domain.SQLDDL},
		{"empty", "   -- only comment", domain.SQLUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.sql); got != tc.want {
				t.Errorf("Classify(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestQuoteIdent(t *testing.T) {
	got, err := QuoteIdent(domain.EngineSQLite, `user"name`)
	if err != nil || got != `"user""name"` {
		t.Fatalf("SQLite QuoteIdent = (%q, %v)", got, err)
	}
	got, err = QuoteIdent(domain.EngineMySQL, "user`name")
	if err != nil || got != "`user``name`" {
		t.Fatalf("MySQL QuoteIdent = (%q, %v)", got, err)
	}
	if _, err := QuoteIdent(domain.EngineSQLite, "bad\x00ident"); err == nil {
		t.Fatal("expected error for NUL byte in identifier")
	}
}

func TestBuildStagedSQLParameterizedAndPreview(t *testing.T) {
	edits := []domain.StagedEdit{
		{
			Kind:   domain.EditUpdate,
			Keys:   []domain.ColumnValue{{Column: "id", Value: domain.CellValue{Text: "2"}}},
			Values: []domain.ColumnValue{{Column: "email", Value: domain.CellValue{Text: "o'reilly@example.com"}}},
		},
		{
			Kind: domain.EditDelete,
			Keys: []domain.ColumnValue{{Column: "id", Value: domain.CellValue{Text: "5"}}},
		},
		{
			Kind: domain.EditInsert,
			Values: []domain.ColumnValue{
				{Column: "email", Value: domain.CellValue{Text: "new@example.com"}},
				{Column: "plan", Value: domain.CellValue{Null: true}},
			},
		},
	}

	stmts, preview, err := BuildStagedSQL(domain.EngineSQLite, "users", edits)
	if err != nil {
		t.Fatal(err)
	}
	wantStmts := []domain.ParamStatement{
		{SQL: `UPDATE "users" SET "email" = ? WHERE "id" = ?`, Args: []any{"o'reilly@example.com", "2"}},
		{SQL: `DELETE FROM "users" WHERE "id" = ?`, Args: []any{"5"}},
		{SQL: `INSERT INTO "users" ("email", "plan") VALUES (?, NULL)`, Args: []any{"new@example.com"}},
	}
	if !reflect.DeepEqual(stmts, wantStmts) {
		t.Fatalf("statements mismatch:\ngot:  %#v\nwant: %#v", stmts, wantStmts)
	}
	wantPreview := "BEGIN;\n" +
		`UPDATE "users" SET "email" = 'o''reilly@example.com' WHERE "id" = '2';` + "\n" +
		`DELETE FROM "users" WHERE "id" = '5';` + "\n" +
		`INSERT INTO "users" ("email", "plan") VALUES ('new@example.com', NULL);` + "\n" +
		"COMMIT;"
	if preview != wantPreview {
		t.Fatalf("preview mismatch:\ngot:\n%s\nwant:\n%s", preview, wantPreview)
	}

	// Update without PK must be rejected.
	_, _, err = BuildStagedSQL(domain.EngineSQLite, "users", []domain.StagedEdit{
		{Kind: domain.EditUpdate, Values: []domain.ColumnValue{{Column: "email", Value: domain.CellValue{Text: "x"}}}},
	})
	if !errors.Is(err, domain.ErrMissingPrimaryKey) {
		t.Fatalf("expected ErrMissingPrimaryKey, got %v", err)
	}
}
