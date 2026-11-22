// Package engine defines KyanDB's database driver contract shared by
// internal/engine/sqlite, postgres, and mysql.
package engine

import (
	"context"
	"net"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
)

// DialFunc dials a network connection, allowing TCP engines to route
// transparently through an SSH tunnel without importing internal/tunnel.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ConnectOptions carries ephemeral credentials and an optional transport dialer.
type ConnectOptions struct {
	Password secret.String
	Dial     DialFunc
}

// Driver opens live database connections for a specific engine.
type Driver interface {
	Engine() domain.Engine
	Connect(ctx context.Context, profile domain.Profile, opts ConnectOptions) (Conn, error)
}

// Conn is an active database connection pool owned exclusively by internal/runtime.
type Conn interface {
	Version(ctx context.Context) (string, error)
	ListObjects(ctx context.Context) ([]domain.SchemaObject, error)
	BrowseTable(ctx context.Context, table, where string, limit, offset int) (domain.QueryResult, error)
	ExecQuery(ctx context.Context, query string) (domain.QueryResult, error)
	ExecTransaction(ctx context.Context, stmts []domain.ParamStatement) (int64, error)
	Close() error
}
