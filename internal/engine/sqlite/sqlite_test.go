package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/sqlguard"
)

func TestSQLiteBrowseQueryStagedCommitAndReadOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	drv := NewDriver()

	rw, err := drv.Connect(ctx, domain.Profile{
		ID:       "local",
		Name:     "local",
		Engine:   domain.EngineSQLite,
		FilePath: dbPath,
		Env:      domain.EnvDev,
	}, engine.ConnectOptions{})
	if err != nil {
		t.Fatalf("Connect RW: %v", err)
	}
	defer rw.Close()

	ver, err := rw.Version(ctx)
	if err != nil || ver == "" {
		t.Fatalf("Version = (%q, %v)", ver, err)
	}

	if _, err := rw.ExecQuery(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, plan TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rw.ExecQuery(ctx, `INSERT INTO users (id, email, plan) VALUES (1, 'ada@example.com', 'team'), (2, 'linus@example.com', NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := rw.ExecQuery(ctx, `CREATE VIEW active_users AS SELECT id, email FROM users`); err != nil {
		t.Fatal(err)
	}

	objs, err := rw.ListObjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 || objs[0].Name != "users" || objs[0].Rows != 2 || objs[1].Name != "active_users" || objs[1].Kind != domain.ObjectView {
		t.Fatalf("unexpected objects: %+v", objs)
	}

	res, err := rw.BrowseTable(ctx, "users", "id = 2", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 || !res.Columns[0].PrimaryKey || res.Rows[0][1].Text != "linus@example.com" || !res.Rows[0][2].Null {
		t.Fatalf("unexpected browse result: %+v", res)
	}

	// Execute a real staged edit transaction through sqlguard.
	stmts, _, err := sqlguard.BuildStagedSQL(domain.EngineSQLite, "users", []domain.StagedEdit{
		{
			Kind:   domain.EditUpdate,
			Keys:   []domain.ColumnValue{{Column: "id", Value: domain.CellValue{Text: "2"}}},
			Values: []domain.ColumnValue{{Column: "plan", Value: domain.CellValue{Text: "pro"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	affected, err := rw.ExecTransaction(ctx, stmts)
	if err != nil || affected != 1 {
		t.Fatalf("ExecTransaction = (%d, %v)", affected, err)
	}

	// Reopen read-only and verify connection-level write protection.
	ro, err := drv.Connect(ctx, domain.Profile{
		ID:       "local-ro",
		Name:     "local-ro",
		Engine:   domain.EngineSQLite,
		FilePath: dbPath,
		Env:      domain.EnvProd,
		ReadOnly: true,
	}, engine.ConnectOptions{})
	if err != nil {
		t.Fatalf("Connect RO: %v", err)
	}
	defer ro.Close()

	check, err := ro.BrowseTable(ctx, "users", "id = 2", 10, 0)
	if err != nil || check.Rows[0][2].Text != "pro" {
		t.Fatalf("expected committed update visible in RO conn, got (%+v, %v)", check, err)
	}
	if _, err := ro.ExecQuery(ctx, `DELETE FROM users WHERE id = 1`); err == nil {
		t.Fatal("expected SQLite read-only connection to reject DELETE")
	}
}
