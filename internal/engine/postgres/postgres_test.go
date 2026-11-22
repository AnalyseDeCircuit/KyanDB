package postgres

import (
	"context"
	"errors"
	"math/big"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
)

func TestBuildPoolConfigDefaultsReadOnlyAndTunnelDial(t *testing.T) {
	if _, err := buildPoolConfig(domain.Profile{Engine: domain.EnginePostgres}, engine.ConnectOptions{}); !errors.Is(err, domain.ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for empty host, got %v", err)
	}

	dialCalled := false
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCalled = true
		return nil, errors.New("dial intercepted: " + addr)
	}

	cfg, err := buildPoolConfig(domain.Profile{
		Name:     "prod-pg",
		Engine:   domain.EnginePostgres,
		Host:     "db.internal",
		Username: "readonly",
		ReadOnly: true,
	}, engine.ConnectOptions{
		Password: secret.NewString("s3cr3t"),
		Dial:     dial,
	})
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.ConnConfig.Port != 5432 || cfg.ConnConfig.Database != "postgres" {
		t.Fatalf("unexpected defaults: port=%d db=%q", cfg.ConnConfig.Port, cfg.ConnConfig.Database)
	}
	if cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] != "on" {
		t.Fatalf("expected default_transaction_read_only=on, got %+v", cfg.ConnConfig.RuntimeParams)
	}
	// Remote hostname resolution must pass through untouched when tunneling.
	addrs, err := cfg.ConnConfig.LookupFunc(context.Background(), "db.internal")
	if err != nil || len(addrs) != 1 || addrs[0] != "db.internal" {
		t.Fatalf("expected pass-through LookupFunc for tunneled host, got (%v, %v)", addrs, err)
	}

	// Verify Driver.Connect invokes the custom DialFunc with remote host:port.
	_, err = NewDriver().Connect(context.Background(), domain.Profile{
		Name:     "prod-pg",
		Engine:   domain.EnginePostgres,
		Host:     "db.internal",
		Port:     5433,
		Username: "readonly",
	}, engine.ConnectOptions{Dial: dial})
	if !dialCalled {
		t.Fatal("expected postgres Driver.Connect to invoke custom DialFunc")
	}
	if err == nil {
		t.Fatal("expected error from stubbed dialer")
	}
}

func TestFormatPGNumeric(t *testing.T) {
	cases := []struct {
		n    pgtype.Numeric
		want string
	}{
		{pgtype.Numeric{Int: big.NewInt(124050), Exp: -2, Valid: true}, "1240.50"},
		{pgtype.Numeric{Int: big.NewInt(-5), Exp: -2, Valid: true}, "-0.05"},
		{pgtype.Numeric{Int: big.NewInt(42), Exp: 2, Valid: true}, "4200"},
	}
	for _, tc := range cases {
		if got := formatPGNumeric(tc.n); got != tc.want {
			t.Errorf("formatPGNumeric(%+v) = %q, want %q", tc.n, got, tc.want)
		}
	}
}
