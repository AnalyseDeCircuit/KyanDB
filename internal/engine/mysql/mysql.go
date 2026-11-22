// Package mysql implements KyanDB's pure-Go MySQL engine using go-sql-driver/mysql.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/secret"
	"github.com/AnalyseDeCircuit/kyandb/internal/sqlguard"
)

// Driver opens pure-Go MySQL connections.
type Driver struct{}

func NewDriver() Driver { return Driver{} }

func (Driver) Engine() domain.Engine { return domain.EngineMySQL }

func (Driver) Connect(ctx context.Context, profile domain.Profile, opts engine.ConnectOptions) (engine.Conn, error) {
	cfg, err := buildConfig(profile, opts)
	if err != nil {
		return nil, err
	}
	connector, err := mysqldrv.NewConnector(cfg)
	if err != nil {
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	return &conn{db: db}, nil
}

func buildConfig(profile domain.Profile, opts engine.ConnectOptions) (*mysqldrv.Config, error) {
	host := strings.TrimSpace(profile.Host)
	if host == "" {
		return nil, fmt.Errorf("%w: empty MySQL host", domain.ErrInvalidProfile)
	}
	port := profile.Port
	if port <= 0 {
		port = 3306
	}
	if port > 65535 {
		return nil, fmt.Errorf("%w: invalid MySQL port %d", domain.ErrInvalidProfile, port)
	}

	cfg := mysqldrv.NewConfig()
	cfg.User = strings.TrimSpace(profile.Username)
	if !opts.Password.Empty() {
		cfg.Passwd = opts.Password.Value()
	}
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(host, strconv.Itoa(port))
	cfg.DBName = strings.TrimSpace(profile.Database)
	cfg.Timeout = 10 * time.Second
	cfg.MultiStatements = false
	cfg.Params = map[string]string{
		"charset": "utf8mb4",
	}
	if profile.ReadOnly {
		cfg.Params["transaction_read_only"] = "1"
	}
	if opts.Dial != nil {
		cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return opts.Dial(ctx, network, addr)
		}
	}
	return cfg, nil
}

type conn struct {
	db *sql.DB
}

func (c *conn) Close() error { return c.db.Close() }

func (c *conn) Version(ctx context.Context) (string, error) {
	var ver string
	if err := c.db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&ver); err != nil {
		return "", errors.New(secret.ScrubDSN(err.Error()))
	}
	if i := strings.IndexByte(ver, '-'); i > 0 {
		return ver[:i], nil
	}
	return ver, nil
}

func (c *conn) ListObjects(ctx context.Context) ([]domain.SchemaObject, error) {
	const tableQuery = `
		SELECT TABLE_NAME, TABLE_TYPE, COALESCE(TABLE_ROWS, -1)
		FROM information_schema.tables
		WHERE TABLE_SCHEMA = DATABASE()
		ORDER BY CASE WHEN TABLE_TYPE = 'BASE TABLE' THEN 0 ELSE 1 END, TABLE_NAME`

	rows, err := c.db.QueryContext(ctx, tableQuery)
	if err != nil {
		return nil, errors.New(secret.ScrubDSN(err.Error()))
	}
	defer rows.Close()

	var objects []domain.SchemaObject
	for rows.Next() {
		var name, typ string
		var est int64
		if err := rows.Scan(&name, &typ, &est); err != nil {
			return nil, err
		}
		kind := domain.ObjectTable
		if strings.Contains(strings.ToUpper(typ), "VIEW") {
			kind = domain.ObjectView
			est = -1
		}
		objects = append(objects, domain.SchemaObject{Name: name, Kind: kind, Rows: est})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	const routineQuery = `
		SELECT ROUTINE_NAME
		FROM information_schema.routines
		WHERE ROUTINE_SCHEMA = DATABASE()
		ORDER BY ROUTINE_NAME`

	if rRows, err := c.db.QueryContext(ctx, routineQuery); err == nil {
		defer rRows.Close()
		for rRows.Next() {
			var name string
			if err := rRows.Scan(&name); err == nil {
				objects = append(objects, domain.SchemaObject{Name: name, Kind: domain.ObjectFunction, Rows: -1})
			}
		}
	}
	return objects, nil
}

func (c *conn) BrowseTable(ctx context.Context, table, where string, limit, offset int) (domain.QueryResult, error) {
	qTable, err := sqlguard.QuoteIdent(domain.EngineMySQL, table)
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
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+qTable+whereClause).Scan(&total); err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}

	rows, err := c.db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s%s LIMIT ? OFFSET ?", qTable, whereClause), limit, offset)
	if err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}
	defer rows.Close()

	res, err := collectSQLRows(rows, pks)
	if err != nil {
		return domain.QueryResult{}, err
	}
	res.Total = total
	res.Elapsed = time.Since(start)
	return res, nil
}

func (c *conn) primaryKeys(ctx context.Context, table string) map[string]bool {
	const pkQuery = `
		SELECT COLUMN_NAME
		FROM information_schema.columns
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_KEY = 'PRI'`

	rows, err := c.db.QueryContext(ctx, pkQuery, table)
	if err != nil {
		return nil
	}
	defer rows.Close()

	pks := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			pks[name] = true
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
	cls := sqlguard.Classify(trimmed)
	if cls == domain.SQLRead {
		rows, err := c.db.QueryContext(ctx, trimmed)
		if err != nil {
			return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
		}
		defer rows.Close()
		res, err := collectSQLRows(rows, nil)
		if err != nil {
			return domain.QueryResult{}, err
		}
		res.Total = int64(len(res.Rows))
		res.Elapsed = time.Since(start)
		return res, nil
	}

	r, err := c.db.ExecContext(ctx, trimmed)
	if err != nil {
		return domain.QueryResult{}, errors.New(secret.ScrubDSN(err.Error()))
	}
	affected, _ := r.RowsAffected()
	return domain.QueryResult{
		Affected: affected,
		Total:    affected,
		Elapsed:  time.Since(start),
	}, nil
}

func (c *conn) ExecTransaction(ctx context.Context, stmts []domain.ParamStatement) (int64, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.New(secret.ScrubDSN(err.Error()))
	}
	var affected int64
	for _, stmt := range stmts {
		res, err := tx.ExecContext(ctx, stmt.SQL, stmt.Args...)
		if err != nil {
			_ = tx.Rollback()
			return 0, errors.New(secret.ScrubDSN(err.Error()))
		}
		if n, err := res.RowsAffected(); err == nil {
			affected += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, errors.New(secret.ScrubDSN(err.Error()))
	}
	return affected, nil
}

func collectSQLRows(rows *sql.Rows, pks map[string]bool) (domain.QueryResult, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return domain.QueryResult{}, err
	}
	cols := make([]domain.Column, len(colTypes))
	for i, ct := range colTypes {
		dbType := strings.ToLower(ct.DatabaseTypeName())
		cols[i] = domain.Column{
			Name:       ct.Name(),
			Type:       dbType,
			Numeric:    isNumericMySQLType(dbType),
			PrimaryKey: pks[ct.Name()],
		}
	}

	raw := make([]sql.RawBytes, len(cols))
	dest := make([]any, len(cols))
	for i := range raw {
		dest[i] = &raw[i]
	}

	var outRows [][]domain.CellValue
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return domain.QueryResult{}, err
		}
		cells := make([]domain.CellValue, len(cols))
		for i, b := range raw {
			if b == nil {
				cells[i] = domain.CellValue{Null: true}
			} else {
				cells[i] = domain.CellValue{Text: string(b)}
			}
		}
		outRows = append(outRows, cells)
	}
	if err := rows.Err(); err != nil {
		return domain.QueryResult{}, err
	}
	return domain.QueryResult{Columns: cols, Rows: outRows}, nil
}

func isNumericMySQLType(t string) bool {
	upper := strings.ToUpper(t)
	for _, tok := range []string{"INT", "DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "BIT"} {
		if strings.Contains(upper, tok) {
			return true
		}
	}
	return false
}
