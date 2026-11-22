package mysql

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
)

func TestBuildConfigDefaultsReadOnlyAndTunnelDial(t *testing.T) {
	if _, err := buildConfig(domain.Profile{Engine: domain.EngineMySQL}, engine.ConnectOptions{}); !errors.Is(err, domain.ErrInvalidProfile) {
		t.Fatalf("expected ErrInvalidProfile for empty host, got %v", err)
	}

	dialCalled := false
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCalled = true
		if addr != "mysql.internal:3307" {
			t.Errorf("unexpected target addr %q", addr)
		}
		return nil, errors.New("dial intercepted")
	}

	cfg, err := buildConfig(domain.Profile{
		Name:     "staging-mysql",
		Engine:   domain.EngineMySQL,
		Host:     "mysql.internal",
		Port:     3307,
		Database: "orders",
		Username: "ops",
		ReadOnly: true,
	}, engine.ConnectOptions{
		Password: secret.NewString("p@ss"),
		Dial:     dial,
	})
	if err != nil {
		t.Fatalf("buildConfig: %v", err)
	}
	if cfg.Addr != "mysql.internal:3307" || cfg.DBName != "orders" || cfg.MultiStatements {
		t.Fatalf("unexpected mysql config: addr=%q db=%q multi=%v", cfg.Addr, cfg.DBName, cfg.MultiStatements)
	}
	if cfg.Params["transaction_read_only"] != "1" {
		t.Fatalf("expected transaction_read_only=1, got %+v", cfg.Params)
	}

	_, err = NewDriver().Connect(context.Background(), domain.Profile{
		Name:     "staging-mysql",
		Engine:   domain.EngineMySQL,
		Host:     "mysql.internal",
		Port:     3307,
		Username: "ops",
	}, engine.ConnectOptions{Dial: dial})
	if !dialCalled {
		t.Fatal("expected mysql Driver.Connect to invoke custom DialFunc")
	}
	if err == nil {
		t.Fatal("expected error from stubbed dialer")
	}
}
