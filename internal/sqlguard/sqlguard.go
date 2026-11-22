// Package sqlguard provides pure, side-effect-free SQL classification,
// dialect identifier quoting, and parameterized staged-edit SQL generation.
package sqlguard

import (
	"fmt"
	"strings"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

// QuoteIdent escapes a database identifier (table, column, schema) using the
// target engine's quoting rules. Empty identifiers and NUL bytes are rejected.
func QuoteIdent(engine domain.Engine, ident string) (string, error) {
	if ident == "" || strings.IndexByte(ident, 0) >= 0 {
		return "", fmt.Errorf("invalid SQL identifier %q", ident)
	}
	if engine == domain.EngineMySQL {
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`", nil
	}
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`, nil
}

// Classify inspects one or more semicolon-separated SQL statements and
// returns the most dangerous class present. Any write, DDL, or unknown
// statement inside a multi-statement script forces the whole script to be
// classified as mutating so read-only gates cannot be bypassed by chaining.
func Classify(sql string) domain.SQLClass {
	stmts := splitStatements(sql)
	if len(stmts) == 0 {
		return domain.SQLUnknown
	}
	worst := domain.SQLRead
	sawAny := false
	for _, stmt := range stmts {
		cls := classifySingle(stmt)
		if cls == domain.SQLUnknown && strings.TrimSpace(stripLeadingNoise(stmt)) == "" {
			continue
		}
		sawAny = true
		if severity(cls) > severity(worst) {
			worst = cls
		}
	}
	if !sawAny {
		return domain.SQLUnknown
	}
	return worst
}

func severity(c domain.SQLClass) int {
	switch c {
	case domain.SQLRead:
		return 1
	case domain.SQLTransaction:
		return 2
	case domain.SQLWrite:
		return 3
	case domain.SQLDDL:
		return 4
	default:
		return 5
	}
}

func classifySingle(stmt string) domain.SQLClass {
	clean := stripLeadingNoise(stmt)
	if clean == "" {
		return domain.SQLUnknown
	}
	verb, rest := nextWord(clean)
	switch verb {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "VALUES":
		return domain.SQLRead
	case "EXPLAIN":
		// EXPLAIN ANALYZE executes its inner statement (e.g. DELETE), so check it.
		sub, after := nextWord(stripLeadingNoise(rest))
		if sub == "ANALYZE" || sub == "QUERY" {
			if sub == "QUERY" {
				// SQLite EXPLAIN QUERY PLAN does not execute mutations.
				return domain.SQLRead
			}
			inner := classifySingle(after)
			if inner.Mutates() {
				return inner
			}
		}
		return domain.SQLRead
	case "PRAGMA":
		// PRAGMA name = value mutates connection/database state; read-only PRAGMAs have no '='.
		if strings.Contains(rest, "=") {
			return domain.SQLWrite
		}
		return domain.SQLRead
	case "WITH":
		return classifyCTE(rest)
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE", "UPSERT", "COPY":
		return domain.SQLWrite
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "VACUUM", "REINDEX", "ATTACH", "DETACH", "GRANT", "REVOKE":
		return domain.SQLDDL
	case "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "SAVEPOINT", "RELEASE":
		return domain.SQLTransaction
	default:
		return domain.SQLUnknown
	}
}

// classifyCTE skips CTE definitions (WITH [RECURSIVE] a AS (...), b AS (...))
// to classify the actual outer statement verb.
func classifyCTE(rest string) domain.SQLClass {
	s := stripLeadingNoise(rest)
	word, after := nextWord(s)
	if word == "RECURSIVE" {
		s = stripLeadingNoise(after)
	}
	depth := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inSingle:
			if ch == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if ch == '"' {
				if i+1 < len(s) && s[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case ch == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			i++
		case ch == '(':
			depth++
		case ch == ')':
			if depth > 0 {
				depth--
			}
			if depth == 0 {
				tail := stripLeadingNoise(s[i+1:])
				if strings.HasPrefix(tail, ",") {
					continue
				}
				return classifySingle(tail)
			}
		}
	}
	return domain.SQLUnknown
}

// splitStatements divides SQL on top-level semicolons outside quotes and comments.
func splitStatements(sql string) []string {
	var out []string
	start := 0
	inSingle, inDouble, inBacktick := false, false, false
	for i := 0; i < len(sql); i++ {
		ch := sql[i]
		switch {
		case inSingle:
			if ch == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			if ch == '"' {
				if i+1 < len(sql) && sql[i+1] == '"' {
					i++
				} else {
					inDouble = false
				}
			}
		case inBacktick:
			if ch == '`' {
				inBacktick = false
			}
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == '`':
			inBacktick = true
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
		case ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i++
		case ch == ';':
			part := strings.TrimSpace(sql[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(sql[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

func stripLeadingNoise(s string) string {
	for {
		s = strings.TrimLeft(s, " \t\r\n")
		switch {
		case strings.HasPrefix(s, "--"):
			if nl := strings.IndexByte(s, '\n'); nl >= 0 {
				s = s[nl+1:]
			} else {
				return ""
			}
		case strings.HasPrefix(s, "/*"):
			if end := strings.Index(s[2:], "*/"); end >= 0 {
				s = s[end+4:]
			} else {
				return ""
			}
		default:
			return s
		}
	}
}

func nextWord(s string) (word, rest string) {
	i := 0
	for i < len(s) && ((s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') || s[i] == '_') {
		i++
	}
	return strings.ToUpper(s[:i]), s[i:]
}

// BuildStagedSQL turns staged edits into parameterized execution statements
// plus a human-reviewable BEGIN...COMMIT SQL preview. Execution always uses
// the parameterized statements; values are never interpolated into executable SQL.
func BuildStagedSQL(engine domain.Engine, table string, edits []domain.StagedEdit) ([]domain.ParamStatement, string, error) {
	if len(edits) == 0 {
		return nil, "", nil
	}
	qTable, err := QuoteIdent(engine, table)
	if err != nil {
		return nil, "", err
	}

	stmts := make([]domain.ParamStatement, 0, len(edits))
	var preview strings.Builder
	preview.WriteString("BEGIN;\n")

	for _, edit := range edits {
		ps, prev, err := buildOneEdit(engine, qTable, edit)
		if err != nil {
			return nil, "", err
		}
		stmts = append(stmts, ps)
		preview.WriteString(prev)
		preview.WriteString(";\n")
	}
	preview.WriteString("COMMIT;")
	return stmts, preview.String(), nil
}

func placeholder(engine domain.Engine, index int) string {
	if engine == domain.EnginePostgres {
		return fmt.Sprintf("$%d", index)
	}
	return "?"
}

func previewLiteral(v domain.CellValue) string {
	if v.Null {
		return "NULL"
	}
	return "'" + strings.ReplaceAll(v.Text, "'", "''") + "'"
}

func buildOneEdit(engine domain.Engine, qTable string, edit domain.StagedEdit) (domain.ParamStatement, string, error) {
	switch edit.Kind {
	case domain.EditUpdate:
		if len(edit.Keys) == 0 {
			return domain.ParamStatement{}, "", domain.ErrMissingPrimaryKey
		}
		if len(edit.Values) == 0 {
			return domain.ParamStatement{}, "", fmt.Errorf("staged update has no modified columns")
		}
		var setSQL, setPrev, whereSQL, wherePrev []string
		var args []any
		argN := 1

		for _, cv := range edit.Values {
			col, err := QuoteIdent(engine, cv.Column)
			if err != nil {
				return domain.ParamStatement{}, "", err
			}
			if cv.Value.Null {
				setSQL = append(setSQL, col+" = NULL")
				setPrev = append(setPrev, col+" = NULL")
			} else {
				setSQL = append(setSQL, col+" = "+placeholder(engine, argN))
				setPrev = append(setPrev, col+" = "+previewLiteral(cv.Value))
				args = append(args, cv.Value.Text)
				argN++
			}
		}
		for _, kv := range edit.Keys {
			col, err := QuoteIdent(engine, kv.Column)
			if err != nil {
				return domain.ParamStatement{}, "", err
			}
			if kv.Value.Null {
				whereSQL = append(whereSQL, col+" IS NULL")
				wherePrev = append(wherePrev, col+" IS NULL")
			} else {
				whereSQL = append(whereSQL, col+" = "+placeholder(engine, argN))
				wherePrev = append(wherePrev, col+" = "+previewLiteral(kv.Value))
				args = append(args, kv.Value.Text)
				argN++
			}
		}
		execSQL := fmt.Sprintf("UPDATE %s SET %s WHERE %s", qTable, strings.Join(setSQL, ", "), strings.Join(whereSQL, " AND "))
		prevSQL := fmt.Sprintf("UPDATE %s SET %s WHERE %s", qTable, strings.Join(setPrev, ", "), strings.Join(wherePrev, " AND "))
		return domain.ParamStatement{SQL: execSQL, Args: args}, prevSQL, nil

	case domain.EditDelete:
		if len(edit.Keys) == 0 {
			return domain.ParamStatement{}, "", domain.ErrMissingPrimaryKey
		}
		var whereSQL, wherePrev []string
		var args []any
		argN := 1
		for _, kv := range edit.Keys {
			col, err := QuoteIdent(engine, kv.Column)
			if err != nil {
				return domain.ParamStatement{}, "", err
			}
			if kv.Value.Null {
				whereSQL = append(whereSQL, col+" IS NULL")
				wherePrev = append(wherePrev, col+" IS NULL")
			} else {
				whereSQL = append(whereSQL, col+" = "+placeholder(engine, argN))
				wherePrev = append(wherePrev, col+" = "+previewLiteral(kv.Value))
				args = append(args, kv.Value.Text)
				argN++
			}
		}
		execSQL := fmt.Sprintf("DELETE FROM %s WHERE %s", qTable, strings.Join(whereSQL, " AND "))
		prevSQL := fmt.Sprintf("DELETE FROM %s WHERE %s", qTable, strings.Join(wherePrev, " AND "))
		return domain.ParamStatement{SQL: execSQL, Args: args}, prevSQL, nil

	case domain.EditInsert:
		if len(edit.Values) == 0 {
			return domain.ParamStatement{}, "", fmt.Errorf("staged insert has no columns")
		}
		var cols, valsSQL, valsPrev []string
		var args []any
		argN := 1
		for _, cv := range edit.Values {
			col, err := QuoteIdent(engine, cv.Column)
			if err != nil {
				return domain.ParamStatement{}, "", err
			}
			cols = append(cols, col)
			if cv.Value.Null {
				valsSQL = append(valsSQL, "NULL")
				valsPrev = append(valsPrev, "NULL")
			} else {
				valsSQL = append(valsSQL, placeholder(engine, argN))
				valsPrev = append(valsPrev, previewLiteral(cv.Value))
				args = append(args, cv.Value.Text)
				argN++
			}
		}
		execSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", qTable, strings.Join(cols, ", "), strings.Join(valsSQL, ", "))
		prevSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", qTable, strings.Join(cols, ", "), strings.Join(valsPrev, ", "))
		return domain.ParamStatement{SQL: execSQL, Args: args}, prevSQL, nil

	default:
		return domain.ParamStatement{}, "", fmt.Errorf("unsupported edit kind %d", edit.Kind)
	}
}
