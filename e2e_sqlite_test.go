package main

import (
	"database/sql"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	_ "modernc.org/sqlite"

	"github.com/AnalyseDeCircuit/kyandb/internal/config"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine/sqlite"
	"github.com/AnalyseDeCircuit/kyandb/internal/runtime"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/app"
)

func seedE2ESQLiteDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "kyandb_e2e.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL,
			status TEXT NOT NULL,
			plan TEXT,
			balance REAL NOT NULL
		);`,
		`INSERT INTO users (id, email, status, plan, balance) VALUES
			(1, 'ada@example.com', 'active', 'team', 1240.50),
			(2, 'linus@example.com', 'invited', 'free', 0.00),
			(3, 'grace@example.com', 'active', NULL, 88.10);`,
		`CREATE VIEW active_users AS SELECT id, email, plan FROM users WHERE status = 'active';`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	return dbPath
}

func saveE2EShot(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("KYANDB_SHOT")
	if dir == "" {
		return
	}
	f, err := os.Create(dir + "-" + name + ".png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndSQLiteVerticalSlice(t *testing.T) {
	dbPath := seedE2ESQLiteDB(t)
	cfgPath := filepath.Join(t.TempDir(), "connections.json")

	rt, err := runtime.New(
		config.NewStore(cfgPath),
		secret.NewMemoryStore(),
		sqlite.NewDriver(),
	)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Errorf("runtime.Close: %v", err)
		}
	}()

	shell := app.NewFromRuntime(rt)
	tt := ui.NewTester(shell.View, 1280, 780)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
	tt.Frame()

	// 1. Fresh app starts in genuine empty state (zero prototype/fixture data).
	if !tt.HasText("No connections") || !tt.HasText("No open tabs") {
		t.Fatalf("expected empty initial state, texts: %q", tt.Texts())
	}

	// 2. Open New Connection modal, select SQLite, fill name & DB path, test connectivity.
	if err := tt.Click("New Connection"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("SQLite"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("local-app")
	if err := tt.Click("Database File"); err != nil {
		t.Fatal(err)
	}
	tt.Type(dbPath)
	if err := tt.Click("Test Connection"); err != nil {
		t.Fatal(err)
	}
	rt.WaitIdle()
	tt.Frame()

	foundTestOK := false
	for _, txt := range tt.Texts() {
		if strings.HasPrefix(txt, "Connected (3.") && strings.HasSuffix(txt, " ms)") {
			foundTestOK = true
			break
		}
	}
	if !foundTestOK {
		t.Fatalf("expected SQLite connectivity test success message, texts: %q", tt.Texts())
	}

	// 3. Save connection -> persists profile and asynchronously loads schema objects.
	if err := tt.Click("Save Connection"); err != nil {
		t.Fatal(err)
	}
	rt.WaitIdle()
	tt.Frame()
	for _, want := range []string{"local-app", "TABLES", "users", "VIEWS", "active_users"} {
		if !tt.HasText(want) {
			t.Fatalf("expected %q after saving SQLite connection, texts: %q", want, tt.Texts())
		}
	}

	// 4. Click "users" table in sidebar -> opens live table grid from SQLite.
	if err := tt.Click("users"); err != nil {
		t.Fatal(err)
	}
	rt.WaitIdle()
	tt.Frame()
	for _, want := range []string{"ada@example.com", "linus@example.com", "grace@example.com", "NULL", "3 rows"} {
		if !tt.HasText(want) {
			t.Fatalf("expected live SQLite row value %q, texts: %q", want, tt.Texts())
		}
	}

	// 5. Filter rows with WHERE bar and submit with Enter.
	if err := tt.Click("Filter rows"); err != nil {
		t.Fatal(err)
	}
	tt.Type("status = 'active'")
	tt.Key(0, ui.KeyEnter)
	rt.WaitIdle()
	tt.Frame()
	if !tt.HasText("ada@example.com") || !tt.HasText("grace@example.com") || tt.HasText("linus@example.com") {
		t.Fatalf("WHERE filter did not restrict rows, texts: %q", tt.Texts())
	}
	// Clear WHERE filter to restore all 3 rows.
	if err := tt.Click("Clear filter"); err != nil {
		t.Fatal(err)
	}
	rt.WaitIdle()
	tt.Frame()
	if !tt.HasText("linus@example.com") {
		t.Fatalf("clearing WHERE filter should restore all rows, texts: %q", tt.Texts())
	}

	// 6. Right-click cell "invited" -> Edit cell inline -> type "suspended" -> Enter.
	if err := tt.RightClick("invited"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Edit cell"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("suspended")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	// Let the ledger bar's BarRevealTransition settle before clicking it.
	tt.Advance(250 * time.Millisecond)

	if !tt.HasText("suspended") || !tt.HasText("1 pending") || !tt.HasText("Review SQL") {
		t.Fatalf("expected inline staged edit + ledger bar, texts: %q", tt.Texts())
	}
	saveE2EShot(t, tt, "e2e-sqlite-staged-dark")

	// 7. Open Review SQL modal -> verify generated UPDATE -> Commit atomically.
	if err := tt.Click("Review SQL"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if !tt.HasText("1 statement in 1 transaction") || !tt.HasText("Copy SQL") {
		t.Fatalf("expected Review SQL modal summary, texts: %q", tt.Texts())
	}
	if err := tt.Click("Commit"); err != nil {
		t.Fatal(err)
	}
	rt.WaitIdle()
	tt.Frame()
	tt.Advance(250 * time.Millisecond)

	if tt.HasText("1 pending") {
		t.Fatalf("expected staged ledger to clear after Commit, texts: %q", tt.Texts())
	}
	if !tt.HasText("suspended") {
		t.Fatalf("expected committed value to reload from SQLite, texts: %q", tt.Texts())
	}

	// Verify persistence independently on disk via fresh database/sql handle.
	verifyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyDB.Close()
	var persisted string
	if err := verifyDB.QueryRow(`SELECT status FROM users WHERE id = 2`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != "suspended" {
		t.Fatalf("expected status='suspended' persisted on disk, got %q", persisted)
	}

	// 8. Open New Query tab, type SELECT, run it, and verify live query results.
	if err := tt.Click("New Query"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Run") || !tt.HasText("Explain") {
		t.Fatalf("expected query tab action bar, texts: %q", tt.Texts())
	}
	rt.RunQuery(1, "SELECT count(*) AS total_users FROM users WHERE status = 'suspended';")
	rt.WaitIdle()
	tt.Frame()
	if !tt.HasText("total_users") || !tt.HasText("1 row") {
		t.Fatalf("expected SQL query results, texts: %q", tt.Texts())
	}
	saveE2EShot(t, tt, "e2e-sqlite-query-dark")
}
