// Package sqlite implements KyanDB's pure-Go SQLite engine using
// modernc.org/sqlite (zero CGO).
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/engine"
	"github.com/AnalyseDeCircuit/kyandb/internal/sqlguard"
)

// Driver opens pure-Go SQLite connections.
type Driver struct{}

func NewDriver() Driver { return Driver{} }

func (Driver) Engine() domain.Engine { return domain.EngineSQLite }

func (Driver) Connect(ctx context.Context, profile domain.Profile, _ engine.ConnectOptions) (engine.Conn, error) {
	path, err := expandPath(profile.FilePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrInvalidProfile, err)
	}
	if path == "" {
		return nil, fmt.Errorf("%w: empty SQLite file path", domain.ErrInvalidProfile)
	}
	if profile.ReadOnly {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("open SQLite read-only: %w", err)
		}
	}

	dsn := buildDSN(path, profile.ReadOnly)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles concurrent writers best with a single writer connection;
	// keeping a small pool also guarantees PRAGMA query_only stays applied.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close() // best-effort cleanup on failed open
		return nil, err
	}
	if profile.ReadOnly {
		if _, err := db.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("enable SQLite read-only pragma: %w", err)
		}
	}
	return &conn{db: db, readOnly: profile.ReadOnly}, nil
}

func expandPath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

func buildDSN(path string, readOnly bool) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := u.Query()
	if readOnly {
		q.Set("mode", "ro")
	}
	q.Set("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}

type conn struct {
	db       *sql.DB
	readOnly bool
}

func (c *conn) Version(ctx context.Context) (string, error) {
	var v string
	if err := c.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

func (c *conn) ListObjects(ctx context.Context) ([]domain.SchemaObject, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT name, type
		FROM sqlite_master
		WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%'
		ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var objs []domain.SchemaObject
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		kind := domain.ObjectTable
		if typ == "view" {
			kind = domain.ObjectView
		}
		objs = append(objs, domain.SchemaObject{Name: name, Kind: kind, Rows: -1})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i := range objs {
		if objs[i].Kind != domain.ObjectTable {
			continue
		}
		qTable, err := sqlguard.QuoteIdent(domain.EngineSQLite, objs[i].Name)
		if err != nil {
			continue
		}
		var n int64
		if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM "+qTable).Scan(&n); err == nil {
			objs[i].Rows = n
		}
	}
	return objs, nil
}

func (c *conn) tableColumns(ctx context.Context, table string) ([]domain.Column, map[string]domain.Column, error) {
	qTable, err := sqlguard.QuoteIdent(domain.EngineSQLite, table)
	if err != nil {
		return nil, nil, err
	}
	rows, err := c.db.QueryContext(ctx, "PRAGMA table_info("+qTable+")")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var cols []domain.Column
	byName := map[string]domain.Column{}
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return nil, nil, err
		}
		col := domain.Column{
			Name:       name,
			Type:       colType,
			Numeric:    isNumericType(colType),
			PrimaryKey: pk > 0,
		}
		cols = append(cols, col)
		byName[name] = col
	}
	return cols, byName, rows.Err()
}

func (c *conn) BrowseTable(ctx context.Context, table, where string, limit, offset int) (domain.QueryResult, error) {
	start := time.Now()
	qTable, err := sqlguard.QuoteIdent(domain.EngineSQLite, table)
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
		// Reject chained statements inside WHERE filters.
		if sqlguard.Classify("SELECT 1 FROM " + qTable + " WHERE " + trimmed).Mutates() {
			return domain.QueryResult{}, fmt.Errorf("invalid WHERE filter expression")
		}
		whereClause = " WHERE " + trimmed
	}

	var total int64
	if err := c.db.QueryRowContext(ctx, "SELECT count(*) FROM "+qTable+whereClause).Scan(&total); err != nil {
		return domain.QueryResult{}, err
	}

	schemaCols, byName, err := c.tableColumns(ctx, table)
	if err != nil {
		return domain.QueryResult{}, err
	}

	query := fmt.Sprintf("SELECT * FROM %s%s LIMIT %d OFFSET %d", qTable, whereClause, limit, offset)
	res, err := c.runSelect(ctx, query, byName)
	if err != nil {
		return domain.QueryResult{}, err
	}
	if len(res.Columns) == 0 && len(schemaCols) > 0 {
		res.Columns = schemaCols
	}
	res.Total = total
	res.Elapsed = time.Since(start)
	return res, nil
}

func (c *conn) ExecQuery(ctx context.Context, query string) (domain.QueryResult, error) {
	start := time.Now()
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return domain.QueryResult{}, domain.ErrEmptySQL
	}
	cls := sqlguard.Classify(trimmed)
	if cls == domain.SQLRead {
		res, err := c.runSelect(ctx, trimmed, nil)
		if err != nil {
			return domain.QueryResult{}, err
		}
		res.Total = int64(len(res.Rows))
		res.Elapsed = time.Since(start)
		return res, nil
	}

	execRes, err := c.db.ExecContext(ctx, trimmed)
	if err != nil {
		return domain.QueryResult{}, err
	}
	affected, _ := execRes.RowsAffected()
	return domain.QueryResult{
		Affected: affected,
		Elapsed:  time.Since(start),
	}, nil
}

func (c *conn) ExecTransaction(ctx context.Context, stmts []domain.ParamStatement) (int64, error) {
	if c.readOnly {
		return 0, domain.ErrReadOnly
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	var totalAffected int64
	for _, st := range stmts {
		res, err := tx.ExecContext(ctx, st.SQL, st.Args...)
		if err != nil {
			_ = tx.Rollback() // best-effort rollback on statement error
			return 0, err
		}
		if n, err := res.RowsAffected(); err == nil {
			totalAffected += n
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return totalAffected, nil
}

func (c *conn) Close() error {
	return c.db.Close()
}

func (c *conn) runSelect(ctx context.Context, query string, colHints map[string]domain.Column) (domain.QueryResult, error) {
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return domain.QueryResult{}, err
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return domain.QueryResult{}, err
	}
	cols := make([]domain.Column, len(colTypes))
	for i, ct := range colTypes {
		name := ct.Name()
		if hint, ok := colHints[name]; ok {
			cols[i] = hint
			continue
		}
		dbType := ct.DatabaseTypeName()
		cols[i] = domain.Column{
			Name:    name,
			Type:    dbType,
			Numeric: isNumericType(dbType),
		}
	}

	rawVals := make([]any, len(cols))
	scanDest := make([]any, len(cols))
	for i := range rawVals {
		scanDest[i] = &rawVals[i]
	}

	var outRows [][]domain.CellValue
	for rows.Next() {
		if err := rows.Scan(scanDest...); err != nil {
			return domain.QueryResult{}, err
		}
		row := make([]domain.CellValue, len(cols))
		for i, v := range rawVals {
			if v == nil {
				row[i] = domain.CellValue{Null: true}
				continue
			}
			switch val := v.(type) {
			case []byte:
				row[i] = domain.CellValue{Text: string(val)}
			case time.Time:
				row[i] = domain.CellValue{Text: val.Format("2006-01-02 15:04:05")}
			default:
				row[i] = domain.CellValue{Text: fmt.Sprint(val)}
			}
		}
		outRows = append(outRows, row)
	}
	if err := rows.Err(); err != nil {
		return domain.QueryResult{}, err
	}
	return domain.QueryResult{Columns: cols, Rows: outRows}, nil
}

func isNumericType(dbType string) bool {
	upper := strings.ToUpper(dbType)
	for _, token := range []string{"INT", "REAL", "FLOA", "DOUB", "NUM", "DEC"} {
		if strings.Contains(upper, token) {
			return true
		}
	}
	return false
}
