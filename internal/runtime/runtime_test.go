package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AnalyseDeCircuit/kyandb/internal/config"
	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine/sqlite"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
)

func seedTestSQLite(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sample.db")
	c, err := sqlite.NewDriver().Connect(context.Background(), domain.Profile{
		ID:       "seed",
		Name:     "seed",
		Engine:   domain.EngineSQLite,
		FilePath: dbPath,
	}, engine.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, stmt := range []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL, status TEXT NOT NULL, plan TEXT)`,
		`INSERT INTO users (id, email, status, plan) VALUES (1, 'ada@example.com', 'active', 'team'), (2, 'linus@example.com', 'active', NULL)`,
	} {
		if _, err := c.ExecQuery(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	return dbPath
}

func TestRuntimeSQLiteEndToEndBrowseQueryStageCommitAndTeardown(t *testing.T) {
	ctx := context.Background()
	dbPath := seedTestSQLite(t)
	cfgStore := config.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	secStore := secret.NewMemoryStore()

	rt, err := New(cfgStore, secStore, sqlite.NewDriver())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	// Save a SQLite profile and connect.
	prof, err := rt.SaveProfile(domain.Profile{
		Name:     "local-sample",
		Engine:   domain.EngineSQLite,
		FilePath: dbPath,
		Env:      domain.EnvDev,
	}, secret.String{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Connect(ctx, prof.ID); err != nil {
		t.Fatal(err)
	}

	snap := rt.Snapshot()
	if len(snap.Connections) != 1 || !snap.Connections[0].Connected || len(snap.Connections[0].Objects) != 1 {
		t.Fatalf("expected 1 connected SQLite profile with 1 table, got %+v", snap.Connections)
	}

	// Open table tab and wait for async load.
	tabIdx := rt.OpenTable("users")
	rt.WaitIdle()
	snap = rt.Snapshot()
	if len(snap.Tabs) != 1 || len(snap.Tabs[tabIdx].Rows) != 2 || snap.Tabs[tabIdx].Total != 2 {
		t.Fatalf("unexpected loaded table snapshot: %+v", snap.Tabs)
	}

	// Stage a cell edit and verify Review SQL preview before commit.
	if err := rt.StageCellEdit(tabIdx, 1, 2, domain.CellValue{Text: "suspended"}); err != nil {
		t.Fatal(err)
	}
	snap = rt.Snapshot()
	if snap.Tabs[tabIdx].Pending.Modified != 1 || !strings.Contains(snap.Tabs[tabIdx].StagedSQL, `UPDATE "users" SET "status" = 'suspended' WHERE "id" = '2';`) {
		t.Fatalf("unexpected staged preview: pending=%+v sql=%q", snap.Tabs[tabIdx].Pending, snap.Tabs[tabIdx].StagedSQL)
	}
	if snap.Tabs[tabIdx].Rows[1].Change != RowModified || !snap.Tabs[tabIdx].Rows[1].Cells[2].Staged {
		t.Fatalf("expected staged row overlay in snapshot, got %+v", snap.Tabs[tabIdx].Rows[1])
	}

	// Commit staged edit inside one real transaction.
	if err := rt.CommitStaged(ctx, tabIdx); err != nil {
		t.Fatal(err)
	}
	snap = rt.Snapshot()
	if snap.Tabs[tabIdx].Pending.Modified != 0 || snap.Tabs[tabIdx].Rows[1].Cells[2].Text != "suspended" {
		t.Fatalf("expected committed value persisted and draft cleared, got %+v", snap.Tabs[tabIdx])
	}

	// Open a SQL query tab, run SELECT, then close tab without disconnecting shared pool.
	qIdx := rt.NewQueryTab(`SELECT email, status FROM users WHERE status = 'suspended'`)
	rt.RunQuery(qIdx, `SELECT email, status FROM users WHERE status = 'suspended'`)
	rt.WaitIdle()
	snap = rt.Snapshot()
	if len(snap.Tabs[qIdx].Rows) != 1 || snap.Tabs[qIdx].Rows[0].Cells[0].Text != "linus@example.com" {
		t.Fatalf("unexpected query result: %+v", snap.Tabs[qIdx].Rows)
	}

	rt.CloseTab(qIdx)
	snap = rt.Snapshot()
	if !snap.Connections[0].Connected || len(snap.Tabs) != 1 {
		t.Fatalf("closing query tab must keep shared connection alive, got connected=%v tabs=%d", snap.Connections[0].Connected, len(snap.Tabs))
	}
}

func TestRuntimeSafetyGateBlocksWritesOnReadOnlyProfile(t *testing.T) {
	ctx := context.Background()
	dbPath := seedTestSQLite(t)
	rt, err := New(nil, secret.NewMemoryStore(), sqlite.NewDriver())
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	prof, err := rt.SaveProfile(domain.Profile{
		Name:     "prod-ro",
		Engine:   domain.EngineSQLite,
		FilePath: dbPath,
		Env:      domain.EnvProd,
		ReadOnly: true,
	}, secret.String{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Connect(ctx, prof.ID); err != nil {
		t.Fatal(err)
	}

	// Read-only blocks DML via RunQuery before hitting the driver and logs audit entry.
	qIdx := rt.NewQueryTab("")
	rt.RunQuery(qIdx, "DELETE FROM users WHERE id = 1")
	rt.WaitIdle()
	snap := rt.Snapshot()
	if snap.Tabs[qIdx].Error != domain.ErrReadOnly.Error() {
		t.Fatalf("expected ErrReadOnly in tab error, got %q", snap.Tabs[qIdx].Error)
	}
	audit := rt.AuditLog()
	if len(audit) != 1 || audit[0].Status != "blocked" || audit[0].Env != domain.EnvProd {
		t.Fatalf("expected blocked audit entry, got %+v", audit)
	}

	// Read-only also rejects staging a cell edit.
	tIdx := rt.OpenTable("users")
	rt.WaitIdle()
	if err := rt.StageCellEdit(tIdx, 0, 2, domain.CellValue{Text: "hacked"}); !errors.Is(err, domain.ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly on StageCellEdit, got %v", err)
	}
}
