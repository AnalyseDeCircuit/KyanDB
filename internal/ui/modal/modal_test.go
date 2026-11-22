package modal

import (
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

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

func TestConnectionModalFlowsAndSecretClearing(t *testing.T) {
	var st ConnectionState
	st.OpenNew()
	var saved ConnectionDraft
	saveCount := 0

	view := func(c *ui.Context) {
		k := theme.For(c.Theme().Dark)
		c.SetTheme(k.UI)
		ui.Box(c).Fill().Background(k.P.Canvas)
		Connection(c, k, &st, ConnectionCallbacks{
			OnSave: func(d ConnectionDraft) {
				saved = d
				saveCount++
			},
		})
	}

	tt := ui.NewTester(view, 1080, 720)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.Frame()

	// Empty name blocks save with inline validation.
	if err := tt.Click("Save Connection"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Name is required.") || saveCount != 0 {
		t.Fatalf("expected validation error, texts: %q", tt.Texts())
	}

	// Fill name and password, switch to SSH Tunnel and Safety tabs.
	if err := tt.Click("Name"); err != nil {
		t.Fatal(err)
	}
	tt.Type("orders-primary")
	if err := tt.Click("Password"); err != nil {
		t.Fatal(err)
	}
	tt.Type("top-secret-pw")
	shoot(t, tt, "conn-general-dark")

	if err := tt.Click("SSH Tunnel"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Connect via SSH tunnel"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("ProxyJump path") || !tt.HasText("SSH Agent") {
		t.Fatalf("expected SSH fields when tunnel enabled, texts: %q", tt.Texts())
	}
	if err := tt.Click("ProxyJump path"); err != nil {
		t.Fatal(err)
	}
	tt.Type("bastion-1")
	shoot(t, tt, "conn-ssh-dark")

	if err := tt.Click("Safety"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("PROD"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Read-only mode"); err != nil {
		t.Fatal(err)
	}
	shoot(t, tt, "conn-safety-dark")

	if err := tt.Click("Save Connection"); err != nil {
		t.Fatal(err)
	}
	if saveCount != 1 {
		t.Fatalf("expected 1 save, got %d", saveCount)
	}
	if saved.Name != "orders-primary" || saved.Env != domain.EnvProd || !saved.ReadOnly || !saved.SSHEnabled || saved.SSHJumpPath != "bastion-1" {
		t.Fatalf("unexpected saved draft: %+v", saved)
	}
	// Modal state must clear ephemeral secrets immediately after closing.
	if st.Open || st.Draft.Password != "" || st.Draft.SSHPassphrase != "" || st.Draft.SSHPassword != "" {
		t.Fatalf("expected modal closed and secrets cleared, got open=%v pw=%q", st.Open, st.Draft.Password)
	}
}

func TestConnectionModalSwitchesEngineToSQLite(t *testing.T) {
	var st ConnectionState
	st.OpenNew()
	view := func(c *ui.Context) {
		k := theme.For(c.Theme().Dark)
		c.SetTheme(k.UI)
		ui.Box(c).Fill().Background(k.P.Canvas)
		Connection(c, k, &st, ConnectionCallbacks{})
	}
	tt := ui.NewTester(view, 1080, 720)
	tt.SetLocale("en")
	tt.Frame()

	if err := tt.Click("SQLite"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Database File") || tt.HasText("Host") {
		t.Fatalf("expected SQLite file field instead of Host, texts: %q", tt.Texts())
	}
}

func TestReviewSQLModalProdAndReadOnly(t *testing.T) {
	const sampleSQL = `BEGIN;

UPDATE "public"."users"
SET "status" = 'suspended'
WHERE "id" = 2;

UPDATE "public"."users"
SET "balance" = 15000.00
WHERE "id" = 4;

DELETE FROM "public"."users"
WHERE "id" = 5;

INSERT INTO "public"."users" ("email", "status", "plan", "balance")
VALUES ('new@example.com', 'invited', 'free', 0.00);

COMMIT;`

	var st ReviewSQLState
	st.OpenWith(ReviewSQLPayload{
		ConnectionName: "orders-primary",
		Env:            domain.EnvProd,
		Modified:       2,
		Inserted:       1,
		Deleted:        1,
		Statements:     4,
		SQL:            sampleSQL,
	})
	committed := false
	view := func(c *ui.Context) {
		k := theme.For(c.Theme().Dark)
		c.SetTheme(k.UI)
		ui.Box(c).Fill().Background(k.P.Canvas)
		ReviewSQL(c, k, &st, ReviewSQLCallbacks{
			OnCommit: func() { committed = true },
		})
	}

	tt := ui.NewTester(view, 1080, 720)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.Frame()

	for _, want := range []string{"Review SQL", "PROD", "orders-primary", "4 statements in 1 transaction", "Changes will execute immediately inside a transaction on orders-primary.", "Commit to prod"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	shoot(t, tt, "review-sql-dark")

	if err := tt.Click("Copy SQL"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Copied") {
		t.Fatalf("expected Copied confirmation after Copy SQL, texts: %q", tt.Texts())
	}
	if err := tt.Click("Commit to prod"); err != nil {
		t.Fatal(err)
	}
	if !committed || st.Open {
		t.Fatalf("expected commit callback and closed modal, committed=%v open=%v", committed, st.Open)
	}

	// Read-only payload suppresses Commit button.
	st.OpenWith(ReviewSQLPayload{
		ConnectionName: "orders-primary",
		Env:            domain.EnvProd,
		ReadOnly:       true,
		Modified:       1,
		Statements:     1,
		SQL:            sampleSQL,
	})
	tt.Frame()
	if !tt.HasText("Read-only connection") || tt.HasText("Commit to prod") {
		t.Fatalf("read-only Review SQL modal should lock commit, texts: %q", tt.Texts())
	}
}

func TestHostKeyModalUnknownAndMismatch(t *testing.T) {
	ch := &domain.HostKeyChallenge{
		Host:        "[bastion-1.internal]:22",
		KeyType:     "ssh-ed25519",
		Fingerprint: "SHA256:4k8m2x9pQvL1nR7sT0wY3zA6bC5dE8fG1hI2jK3lM4n",
	}
	var trustedSave *bool
	rejected := false
	view := func(c *ui.Context) {
		k := theme.For(c.Theme().Dark)
		c.SetTheme(k.UI)
		ui.Box(c).Fill().Background(k.P.Canvas)
		HostKey(c, k, ch, HostKeyCallbacks{
			OnTrust:  func(save bool) { trustedSave = &save },
			OnReject: func() { rejected = true },
		})
	}

	tt := ui.NewTester(view, 960, 640)
	tt.SetLocale("en")
	tt.SetDark(true)
	tt.Frame()
	for _, want := range []string{"Verify SSH Host Key", "[bastion-1.internal]:22", "ssh-ed25519", ch.Fingerprint, "Trust Once", "Trust & Save"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Trust Once"); err != nil {
		t.Fatal(err)
	}
	if trustedSave == nil || *trustedSave {
		t.Fatalf("expected TrustOnce (save=false), got %v", trustedSave)
	}

	// Mismatch mode displays warning banner and previous known fingerprint.
	ch.Mismatch = true
	ch.KnownFingerprint = "SHA256:OldTrustedKeyFingerprint00000000000000000000"
	tt.Frame()
	for _, want := range []string{"SSH Host Key Changed", "Known Fingerprint", ch.KnownFingerprint} {
		if !tt.HasText(want) {
			t.Errorf("mismatch mode missing %q in %q", want, tt.Texts())
		}
	}
	shoot(t, tt, "host-key-mismatch-dark")

	if err := tt.Click("Trust & Save"); err != nil {
		t.Fatal(err)
	}
	if trustedSave == nil || !*trustedSave {
		t.Fatalf("expected TrustAndSave (save=true), got %v", trustedSave)
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	if !rejected {
		t.Fatal("expected Cancel to trigger OnReject")
	}

	// Verify Chinese locale wiring.
	tt.SetLocale("zh-Hans")
	tt.Frame()
	for _, want := range []string{"SSH 主机密钥已变更", "原记录指纹", "仅本次信任", "信任并保存"} {
		if !tt.HasText(want) {
			t.Errorf("zh-Hans missing %q in %q", want, tt.Texts())
		}
	}
}
