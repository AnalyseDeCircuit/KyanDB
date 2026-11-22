package config

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

func TestStoreMissingFileReturnsEmptyAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "profiles.json")
	st := NewStore(path)

	got, err := st.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty profiles on missing file, got %+v", got)
	}

	want := []domain.Profile{
		{
			ID:        "sqlite-1",
			Name:      "local-app",
			Engine:    domain.EngineSQLite,
			FilePath:  "/tmp/app.db",
			Env:       domain.EnvDev,
			ReadOnly:  true,
			SecretRef: "",
		},
		{
			ID:          "pg-1",
			Name:        "orders-primary",
			Engine:      domain.EnginePostgres,
			Host:        "10.0.4.12",
			Port:        5432,
			Database:    "orders",
			Username:    "readonly",
			SecretRef:   "kyandb:conn:pg-1",
			Env:         domain.EnvProd,
			ReadOnly:    true,
			SSHEnabled:  true,
			SSHJumpPath: "bastion-1",
		},
	}
	if err := st.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := st.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("loaded profiles mismatch:\ngot:  %+v\nwant: %+v", loaded, want)
	}
}
