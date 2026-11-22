package app

import (
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

// fixture lives in tests only: prototype data must never ship in the app.
func fixture() Snapshot {
	cols := []Column{
		{Name: "id", Type: "int8", Numeric: true, Key: true},
		{Name: "email", Type: "text"},
		{Name: "status", Type: "text"},
		{Name: "plan", Type: "text"},
		{Name: "balance", Type: "numeric(12,2)", Numeric: true},
		{Name: "created_at", Type: "timestamptz"},
	}
	cell := func(s string) Cell { return Cell{Text: s} }
	null := Cell{Null: true}
	rows := []Row{
		{Cells: []Cell{cell("1"), cell("ada@example.com"), cell("active"), cell("team"), cell("1,240.00"), cell("2026-03-01 09:12:44")}},
		{Cells: []Cell{cell("2"), cell("linus@example.com"), {Text: "suspended", Staged: true}, cell("free"), cell("0.00"), cell("2026-03-02 17:40:03")}, Change: Modified},
		{Cells: []Cell{cell("3"), cell("grace@example.com"), cell("active"), null, cell("88.10"), cell("2026-03-05 11:05:21")}},
		{Cells: []Cell{cell("4"), cell("ken@example.com"), cell("active"), cell("enterprise"), {Text: "15,000.00", Staged: true}, cell("2026-03-09 08:30:00")}, Change: Modified},
		{Cells: []Cell{cell("5"), cell("barbara@example.com"), cell("invited"), cell("team"), cell("0.00"), cell("2026-03-11 14:02:57")}, Change: Deleted},
		{Cells: []Cell{cell("6"), cell("dennis@example.com"), cell("active"), cell("free"), cell("12.00"), null}},
		{Cells: []Cell{cell("7"), cell("margaret@example.com"), cell("active"), cell("team"), cell("310.45"), cell("2026-03-14 07:45:10")}},
		{Cells: []Cell{null, cell("new@example.com"), cell("invited"), cell("free"), cell("0.00"), null}, Change: Inserted},
	}
	queryCols := []Column{
		{Name: "plan", Type: "text"},
		{Name: "users", Type: "int8", Numeric: true},
		{Name: "balance", Type: "numeric(12,2)", Numeric: true},
	}
	queryRows := []Row{
		{Cells: []Cell{cell("enterprise"), cell("412"), cell("6,180,000.00")}},
		{Cells: []Cell{cell("team"), cell("18,920"), cell("23,460,800.00")}},
		{Cells: []Cell{cell("free"), cell("109,070"), cell("0.00")}},
	}
	return Snapshot{
		Generation: 1,
		Active:     0,
		Connections: []Connection{
			{
				Name: "orders-primary", Engine: domain.EnginePostgres, Version: "16.4",
				Address: "10.0.4.12:5432", Via: "bastion-1", Env: domain.EnvProd,
				Objects: []Object{
					{Name: "users", Kind: ObjectTable, Rows: 128_402},
					{Name: "orders", Kind: ObjectTable, Rows: 4_210_337},
					{Name: "order_items", Kind: ObjectTable, Rows: 11_980_114},
					{Name: "invoices", Kind: ObjectTable, Rows: 902_118},
					{Name: "payments", Kind: ObjectTable, Rows: 875_004},
					{Name: "plans", Kind: ObjectTable, Rows: 4},
					{Name: "audit_log", Kind: ObjectTable, Rows: -1},
					{Name: "active_users", Kind: ObjectView, Rows: -1},
					{Name: "monthly_revenue", Kind: ObjectView, Rows: -1},
					{Name: "refresh_rollups", Kind: ObjectFunction, Rows: -1},
				},
			},
			{
				Name: "local-dev", Engine: domain.EngineSQLite, Version: "3.46",
				Address: "~/dev/app.db", Env: domain.EnvDev,
				Objects: []Object{{Name: "notes", Kind: ObjectTable, Rows: 42}},
			},
		},
		Tabs: []Tab{
			{
				Kind: TabTable, Title: "users", Columns: cols, Rows: rows, Total: 128_402, Elapsed: 14 * time.Millisecond,
				Pending:   Pending{Modified: 2, Inserted: 1, Deleted: 1},
				StagedSQL: "BEGIN;\nUPDATE \"users\" SET \"status\" = 'suspended' WHERE \"id\" = 2;\nUPDATE \"users\" SET \"balance\" = 15000.00 WHERE \"id\" = 4;\nDELETE FROM \"users\" WHERE \"id\" = 5;\nINSERT INTO \"users\" (\"email\", \"status\", \"plan\", \"balance\") VALUES ('new@example.com', 'invited', 'free', 0.00);\nCOMMIT;",
			},
			{Kind: TabQuery, Title: "active by plan", Query: "SELECT plan, count(*) AS users, sum(balance) AS balance\nFROM users\nWHERE status = 'active'\nGROUP BY plan\nORDER BY users DESC;", Columns: queryCols, Rows: queryRows, Elapsed: 1240 * time.Millisecond},
		},
	}
}

func shoot(t *testing.T, tt *ui.Tester, name string) {
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

func TestShellRendersWorkspace(t *testing.T) {
	for _, dark := range []bool{false, true} {
		sh := New(fixture)
		tt := ui.NewTester(sh.View, 1280, 780)
		tt.SetLocale("en")
		tt.SetDark(dark)
		// Simulate the macOS traffic lights the hidden title bar leaves.
		tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
		tt.Frame()
		for _, want := range []string{"PROD", "orders-primary", "via bastion-1", "TABLES", "128K", "ada@example.com", "NULL", "4 pending", "Commit to prod", "8 of 128,402 rows", "14 ms"} {
			if !tt.HasText(want) {
				t.Errorf("dark=%v: missing %q in %q", dark, want, tt.Texts())
			}
		}
		mode := "light"
		if dark {
			mode = "dark"
		}
		if err := tt.Click("grace@example.com"); err != nil {
			t.Fatal(err)
		}
		shoot(t, tt, "table-"+mode)

		if err := tt.Click("active by plan"); err != nil {
			t.Fatal(err)
		}
		if !tt.HasText("Run") || !tt.HasText("1.24 s") || tt.HasText("4 pending") {
			t.Errorf("dark=%v: query tab should show Run and no ledger, texts: %q", dark, tt.Texts())
		}
		shoot(t, tt, "query-"+mode)
	}
}

func TestShellConnectionPopoverSwitcher(t *testing.T) {
	selected := -1
	newClicked := false
	sh := NewWithActions(fixture, Actions{
		SelectConnection: func(i int) { selected = i },
		NewConnection:    func() { newClicked = true },
	})
	tt := ui.NewTester(sh.View, 1280, 780)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
	tt.Frame()

	if err := tt.Click("Switch connection"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("local-dev") || !tt.HasText("~/dev/app.db") || !tt.HasText("New Connection") {
		t.Fatalf("expected connection switcher popover items, texts: %q", tt.Texts())
	}
	shoot(t, tt, "switcher-dark")

	if err := tt.Click("local-dev"); err != nil {
		t.Fatal(err)
	}
	if selected != 1 {
		t.Fatalf("expected SelectConnection(1), got %d", selected)
	}
	// Re-open and trigger New Connection.
	if err := tt.Click("Switch connection"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("New Connection"); err != nil {
		t.Fatal(err)
	}
	if !newClicked {
		t.Fatal("expected NewConnection callback to fire")
	}
	if !tt.HasText("General") || !tt.HasText("SSH Tunnel") || !tt.HasText("Safety") {
		t.Fatalf("expected Connection modal to open over shell, texts: %q", tt.Texts())
	}
}

func TestShellReviewSQLModalIntegration(t *testing.T) {
	committed := -1
	sh := NewWithActions(fixture, Actions{
		CommitStaged: func(tabIdx int) { committed = tabIdx },
	})
	tt := ui.NewTester(sh.View, 1280, 780)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
	tt.Frame()

	if err := tt.Click("Review SQL"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("4 statements in 1 transaction") || !tt.HasText("Copy SQL") {
		t.Fatalf("expected Review SQL modal open over workspace, texts: %q", tt.Texts())
	}
	shoot(t, tt, "shell-review-modal-dark")

	if err := tt.Click("Commit to prod"); err != nil {
		t.Fatal(err)
	}
	if committed != 0 {
		t.Fatalf("expected CommitStaged(0) from Review SQL modal, got %d", committed)
	}
}

func TestShellOperationalStatesAndReadOnlyGate(t *testing.T) {
	cancelled := -1
	retried := false
	dismissed := -1
	snap := fixture()
	snap.Connections[0].ReadOnly = true
	snap.Tabs[1].Running = true
	snap.Tabs[1].Error = `ERROR: syntax error at or near "FORM" (SQLSTATE 42601)`

	sh := NewWithActions(func() Snapshot { return snap }, Actions{
		CancelQuery:  func(i int) { cancelled = i },
		RunQuery:     func(int, string) { retried = true },
		DismissError: func(i int) { dismissed = i },
	})
	tt := ui.NewTester(sh.View, 1280, 780)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
	tt.Frame()

	// Read-only connection shows explicit badge and suppresses Commit.
	if !tt.HasText("Read-only") || !tt.HasText("Read-only connection") || tt.HasText("Commit to prod") {
		t.Fatalf("read-only ledger gate failed, texts: %q", tt.Texts())
	}

	// Empty sidebar filter shows explicit prompt + Clear filter.
	if err := tt.Click("Filter objects"); err != nil {
		t.Fatal(err)
	}
	tt.Type("does_not_exist")
	if !tt.HasText("No matching objects") || !tt.HasText("Clear filter") {
		t.Fatalf("expected empty sidebar filter prompt, texts: %q", tt.Texts())
	}
	if err := tt.Click("Clear filter"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("users") {
		t.Fatalf("expected Clear filter to restore schema list, texts: %q", tt.Texts())
	}

	// Switch to query tab: verify Running/Cancel and Error banner Retry/Dismiss.
	if err := tt.Click("active by plan"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Cancel") || !tt.HasText("Running…") || !tt.HasText(`ERROR: syntax error at or near "FORM" (SQLSTATE 42601)`) {
		t.Fatalf("expected running + error states on query tab, texts: %q", tt.Texts())
	}
	shoot(t, tt, "error-running-dark")

	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Retry"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Dismiss"); err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 || !retried || dismissed != 1 {
		t.Fatalf("callbacks mismatch: cancelled=%d retried=%v dismissed=%d", cancelled, retried, dismissed)
	}
}

// TestShellFollowsWindowLocale guards the catalog wiring: messages, plural
// forms, select branches and number formats all follow the window's
// language rather than falling back to English.
func TestShellFollowsWindowLocale(t *testing.T) {
	sh := New(fixture)
	tt := ui.NewTester(sh.View, 1280, 780)
	tt.SetLocale("zh-Hans")
	tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
	tt.Frame()
	for _, want := range []string{"生产", "经由 bastion-1", "表", "13万", "4 项待提交", "提交到生产", "8 / 128,402 行", "14 毫秒"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	for _, english := range []string{"PROD", "TABLES", "4 pending", "Commit to prod"} {
		if tt.HasText(english) {
			t.Errorf("English %q leaked into zh-Hans view", english)
		}
	}
	shoot(t, tt, "table-zh")
}

func TestShellEmptyState(t *testing.T) {
	sh := New(func() Snapshot { return Snapshot{Active: -1} })
	tt := ui.NewTester(sh.View, 1200, 760)
	tt.SetLocale("en")
	tt.Frame()
	for _, want := range []string{"No connection", "No connections", "New Connection", "No open tabs"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	shoot(t, tt, "empty")
}

func TestShellWelcomeQuickLaunch(t *testing.T) {
	for _, dark := range []bool{false, true} {
		snap := fixture()
		snap.Tabs = nil
		sh := New(func() Snapshot { return snap })
		tt := ui.NewTester(sh.View, 1280, 780)
		tt.SetLocale("en")
		tt.SetDark(dark)
		tt.SetTitleBar(ui.TitleBar{Height: 28, Left: 76})
		tt.Frame()
		for _, want := range []string{"KyanDB", "Connections", "Start", "orders-primary", "local-dev", "~/dev/app.db", "New Connection", "New Query"} {
			if !tt.HasText(want) {
				t.Errorf("dark=%v: missing %q in %q", dark, want, tt.Texts())
			}
		}
		if dark {
			shoot(t, tt, "welcome-dark")
		} else {
			shoot(t, tt, "welcome-light")
		}
	}
}
