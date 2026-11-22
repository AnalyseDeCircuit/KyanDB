// Package postgres implements KyanDB's pure-Go PostgreSQL engine using pgx/v5.
package postgres

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/sqlguard"
)

// Driver opens pure-Go PostgreSQL pools.
type Driver struct{}

func NewDriver() Driver { return Driver{} }

func (Driver) Engine() domain.Engine { return domain.EnginePostgres }

func (Driver) Connect(ctx context.Context, profile domain.Profile, opts engine.ConnectOptions) (engine.Conn, error) {
	cfg, err := buildPoolConfig(profile, opts)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	return &conn{pool: pool}, nil
}

func buildPoolConfig(profile domain.Profile, opts engine.ConnectOptions) (*pgxpool.Config, error) {
	host := strings.TrimSpace(profile.Host)
	if host == "" {
		return nil, fmt.Errorf("%w: empty PostgreSQL host", domain.ErrInvalidProfile)
	}
	port := profile.Port
	if port <= 0 {
		port = 5432
	}
	if port > 65535 {
		return nil, fmt.Errorf("%w: invalid PostgreSQL port %d", domain.ErrInvalidProfile, port)
	}
	dbName := strings.TrimSpace(profile.Database)
	if dbName == "" {
		dbName = "postgres"
	}

	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0
	cfg.ConnConfig.Host = host
	cfg.ConnConfig.Port = uint16(port)
	cfg.ConnConfig.Database = dbName
	cfg.ConnConfig.User = strings.TrimSpace(profile.Username)
	if !opts.Password.Empty() {
		cfg.ConnConfig.Password = opts.Password.Value()
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "KyanDB"
	if profile.ReadOnly {
		cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	if opts.Dial != nil {
		cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return opts.Dial(ctx, network, addr)
		}
		// Resolve internal database hostnames on the SSH exit host rather than
		// locally on the workstation.
		cfg.ConnConfig.LookupFunc = func(_ context.Context, h string) ([]string, error) {
			return []string{h}, nil
		}
	}
	return cfg, nil
}

type conn struct {
	pool *pgxpool.Pool
}

func (c *conn) Close() error {
	c.pool.Close()
	return nil
}

func (c *conn) Version(ctx context.Context) (string, error) {
	var ver string
	if err := c.pool.QueryRow(ctx, "SHOW server_version").Scan(&ver); err != nil {
		return "", errors.New(secret.ScrubDSN(err.Error()))
	}
	if i := strings.IndexByte(ver, ' '); i > 0 {
		return ver[:i], nil
	}
	return ver, nil
}

func (c *conn) ListObjects(ctx context.Context) ([]domain.SchemaObject, error) {
	const relQuery = `
		SELECT c.relname, c.relkind::text, COALESCE(c.reltuples::bigint, -1)
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY(current_schemas(false))
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND c.relkind IN ('r', 'p', 'v', 'm')
		ORDER BY CASE WHEN c.relkind IN ('r', 'p') THEN 0 ELSE 1 END, c.relname`

	rows, err := c.pool.Query(ctx, relQuery)
	if err != nil {
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	defer rows.Close()

	var objects []domain.SchemaObject
	for rows.Next() {
		var name, relKind string
		var est int64
		if err := rows.Scan(&name, &relKind, &est); err != nil {
			return nil, err
		}
		kind := domain.ObjectTable
		if relKind == "v" || relKind == "m" {
			kind = domain.ObjectView
			est = -1
		}
		objects = append(objects, domain.SchemaObject{Name: name, Kind: kind, Rows: est})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const fnQuery = `
		SELECT DISTINCT p.proname
		FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = ANY(current_schemas(false))
		  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		ORDER BY p.proname`

	fnRows, err := c.pool.Query(ctx, fnQuery)
	if err == nil {
		defer fnRows.Close()
		for fnRows.Next() {
			var name string
			if err := fnRows.Scan(&name); err == nil {
				objects = append(objects, domain.SchemaObject{Name: name, Kind: domain.ObjectFunction, Rows: -1})
			}
		}
	}
	return objects, nil
}

func (c *conn) BrowseTable(ctx context.Context, table, where string, limit, offset int) (domain.QueryResult, error) {
	qTable, err := sqlguard.QuoteIdent(domain.EnginePostgres, table)
	if err != nil {
		return domain.QueryResult{}, err
	}
	if limit <= 0 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}

	whereClause := ""
	if trimmed := strings.TrimSpace(where); trimmed != "" {
		if sqlguard.Classify("SELECT 1 FROM "+qTable+" WHERE "+trimmed) != domain.SQLRead {
			return domain.QueryResult{}, fmt.Errorf("WHERE filter must be a read-only expression")
		}
		whereClause = " WHERE " + trimmed
	}

	pks := c.primaryKeys(ctx, table)

	start := time.Now()
	var total int64
	if err := c.pool.QueryRow(ctx, "SELECT count(*) FROM "+qTable+whereClause).Scan(&total); err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}

	rows, err := c.pool.Query(ctx, fmt.Sprintf("SELECT * FROM %s%s LIMIT $1 OFFSET $2", qTable, whereClause), limit, offset)
	if err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}
	defer rows.Close()

	res, err := collectRows(rows, pks)
	if err != nil {
		return domain.QueryResult{}, err
	}
	res.Total = total
	res.Elapsed = time.Since(start)
	return res, nil
}

func (c *conn) primaryKeys(ctx context.Context, table string) map[string]bool {
	const pkQuery = `
		SELECT a.attname
		FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class cls ON cls.oid = i.indrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = cls.relnamespace
		JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indisprimary
		  AND cls.relname = $1
		  AND n.nspname = ANY(current_schemas(false))`

	rows, err := c.pool.Query(ctx, pkQuery, table)
	if err != nil {
		return nil
	}
	defer rows.Close()

	pks := map[string]bool{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err == nil {
			pks[col] = true
		}
	}
	return pks
}

func (c *conn) ExecQuery(ctx context.Context, query string) (domain.QueryResult, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return domain.QueryResult{}, domain.ErrEmptySQL
	}

	start := time.Now()
	rows, err := c.pool.Query(ctx, trimmed)
	if err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}
	defer rows.Close()

	res, err := collectRows(rows, nil)
	if err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}
	tag := rows.CommandTag()
	res.Affected = tag.RowsAffected()
	if len(res.Columns) == 0 && res.Affected >= 0 {
		res.Total = res.Affected
	} else {
		res.Total = int64(len(res.Rows))
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

func (c *conn) ExecTransaction(ctx context.Context, stmts []domain.ParamStatement) (int64, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return 0, errors.New(secret.ScrubDSN(err.Error()))
	}
	var affected int64
	for _, stmt := range stmts {
		tag, err := tx.Exec(ctx, stmt.SQL, stmt.Args...)
		if err != nil {
			_ = tx.Rollback(ctx)
			return 0, errors.New(secret.ScrubDSN(err.Error()))
		}
		affected += tag.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, errors.New(secret.ScrubDSN(err.Error()))
	}
	return affected, nil
}

func collectRows(rows pgx.Rows, pks map[string]bool) (domain.QueryResult, error) {
	fds := rows.FieldDescriptions()
	cols := make([]domain.Column, len(fds))
	for i, fd := range fds {
		typeName, numeric := pgOIDInfo(fd.DataTypeOID)
		cols[i] = domain.Column{
			Name:       fd.Name,
			Type:       typeName,
			Numeric:    numeric,
			PrimaryKey: pks[fd.Name],
		}
	}

	var outRows [][]domain.CellValue
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return domain.QueryResult{}, err
		}
		row := make([]domain.CellValue, len(vals))
		for i, v := range vals {
			row[i] = formatPGValue(v)
		}
		outRows = append(outRows, row)
	}
	if err := rows.Err(); err != nil {
		return domain.QueryResult{}, err
	}
	return domain.QueryResult{Columns: cols, Rows: outRows}, nil
}

func formatPGValue(v any) domain.CellValue {
	if v == nil {
		return domain.CellValue{Null: true}
	}
	switch x := v.(type) {
	case string:
		return domain.CellValue{Text: x}
	case []byte:
		return domain.CellValue{Text: "\\x" + hex.EncodeToString(x)}
	case [16]byte:
		return domain.CellValue{Text: fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])}
	case time.Time:
		return domain.CellValue{Text: x.Format("2006-01-02 15:04:05")}
	case pgtype.Numeric:
		if !x.Valid {
			return domain.CellValue{Null: true}
		}
		return domain.CellValue{Text: formatPGNumeric(x)}
	default:
		return domain.CellValue{Text: fmt.Sprint(x)}
	}
}

func formatPGNumeric(n pgtype.Numeric) string {
	if n.NaN {
		return "NaN"
	}
	if n.InfinityModifier == pgtype.Infinity {
		return "Infinity"
	}
	if n.InfinityModifier == pgtype.NegativeInfinity {
		return "-Infinity"
	}
	if n.Int == nil {
		return "0"
	}
	if n.Exp >= 0 {
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.Exp)), nil)
		return new(big.Int).Mul(n.Int, scale).String()
	}
	neg := n.Int.Sign() < 0
	digits := new(big.Int).Abs(n.Int).String()
	dec := int(-n.Exp)
	for len(digits) <= dec {
		digits = "0" + digits
	}
	split := len(digits) - dec
	res := digits[:split] + "." + digits[split:]
	if neg {
		return "-" + res
	}
	return res
}

func pgOIDInfo(oid uint32) (name string, numeric bool) {
	switch oid {
	case pgtype.BoolOID:
		return "bool", false
	case pgtype.Int2OID:
		return "int2", true
	case pgtype.Int4OID:
		return "int4", true
	case pgtype.Int8OID:
		return "int8", true
	case pgtype.Float4OID:
		return "float4", true
	case pgtype.Float8OID:
		return "float8", true
	case pgtype.NumericOID:
		return "numeric", true
	case pgtype.TextOID:
		return "text", false
	case pgtype.VarcharOID:
		return "varchar", false
	case pgtype.UUIDOID:
		return "uuid", false
	case pgtype.TimestampOID:
		return "timestamp", false
	case pgtype.TimestamptzOID:
		return "timestamptz", false
	case pgtype.DateOID:
		return "date", false
	case pgtype.JSONOID:
		return "json", false
	case pgtype.JSONBOID:
		return "jsonb", false
	case pgtype.ByteaOID:
		return "bytea", false
	default:
		return fmt.Sprintf("oid(%d)", oid), false
	}
}
