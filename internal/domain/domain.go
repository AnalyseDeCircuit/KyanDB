// Package domain holds pure value types and sentinel errors shared across
// KyanDB layers. It must import the Go standard library only.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors returned by core layers and translated by internal/ui.
var (
	ErrReadOnly          = errors.New("connection is read-only")
	ErrNotConnected      = errors.New("connection is not active")
	ErrUnsupportedEngine = errors.New("unsupported database engine")
	ErrInvalidProfile    = errors.New("invalid connection profile")
	ErrMissingPrimaryKey = errors.New("table has no primary key for safe row update")
	ErrSecretNotFound    = errors.New("secret not found")
	ErrEmptySQL          = errors.New("SQL statement is empty")
	ErrHostKeyUnknown    = errors.New("SSH host key is not yet trusted")
	ErrHostKeyMismatch   = errors.New("SSH host key does not match known_hosts")
)

// Engine identifies a supported database engine.
type Engine string

const (
	EnginePostgres Engine = "PostgreSQL"
	EngineMySQL    Engine = "MySQL"
	EngineSQLite   Engine = "SQLite"
)

// Environment tags a connection so production safety chrome can be enforced
// consistently by the runtime and reflected by the UI.
type Environment string

const (
	EnvDev     Environment = "dev"
	EnvStaging Environment = "staging"
	EnvProd    Environment = "prod"
)

// Protected reports whether writes against this environment require extra
// visual warning and explicit confirmation.
func (e Environment) Protected() bool {
	return e == EnvStaging || e == EnvProd
}

// SSHAuthKind identifies how KyanDB authenticates to an SSH tunnel hop.
type SSHAuthKind string

const (
	SSHAuthAgent    SSHAuthKind = "agent"
	SSHAuthKey      SSHAuthKind = "key"
	SSHAuthPassword SSHAuthKind = "password"
)

// Profile is a non-sensitive connection definition persisted to user config.
// Passwords, private keys, and passphrases never live here; SecretRef and
// SSHSecretRef point to opaque entries in the OS keychain store.
type Profile struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Engine       Engine      `json:"engine"`
	FilePath     string      `json:"filePath,omitempty"`
	Host         string      `json:"host,omitempty"`
	Port         int         `json:"port,omitempty"`
	Database     string      `json:"database,omitempty"`
	Username     string      `json:"username,omitempty"`
	SecretRef    string      `json:"secretRef,omitempty"`
	Env          Environment `json:"env"`
	ReadOnly     bool        `json:"readOnly,omitempty"`
	SSHEnabled   bool        `json:"sshEnabled,omitempty"`
	SSHJumpPath  string      `json:"sshJumpPath,omitempty"`
	SSHHost      string      `json:"sshHost,omitempty"`
	SSHPort      int         `json:"sshPort,omitempty"`
	SSHUser      string      `json:"sshUser,omitempty"`
	SSHAuth      SSHAuthKind `json:"sshAuth,omitempty"`
	SSHKeyPath   string      `json:"sshKeyPath,omitempty"`
	SSHSecretRef string      `json:"sshSecretRef,omitempty"`
}

// DisplayAddress returns a credential-free host/path string for UI chrome.
func (p Profile) DisplayAddress() string {
	if p.Engine == EngineSQLite {
		return p.FilePath
	}
	if p.Host != "" && p.Port > 0 {
		return fmt.Sprintf("%s:%d", p.Host, p.Port)
	}
	return p.Host
}

// HostKeyChallenge describes an untrusted or changed SSH host key presented
// during handshake so the user can verify its fingerprint before trusting it.
type HostKeyChallenge struct {
	Host             string
	KeyType          string
	Fingerprint      string
	Mismatch         bool
	KnownFingerprint string
}

// ObjectKind groups schema objects in the sidebar.
type ObjectKind int

const (
	ObjectTable ObjectKind = iota
	ObjectView
	ObjectFunction
)

// SchemaObject describes one browsable table, view, or routine.
// Rows is an estimate or exact count; negative means unknown.
type SchemaObject struct {
	Name string
	Kind ObjectKind
	Rows int64
}

// Column describes one result column.
type Column struct {
	Name       string
	Type       string
	Numeric    bool
	PrimaryKey bool
}

// CellValue holds one typed string representation from a result set.
// Null is distinct from an empty string.
type CellValue struct {
	Text string
	Null bool
}

// QueryResult is the materialized result of one browsed table or SQL query.
type QueryResult struct {
	Columns  []Column
	Rows     [][]CellValue
	Total    int64
	Affected int64
	Elapsed  time.Duration
}

// SQLClass categorizes a SQL statement for runtime safety gating.
type SQLClass int

const (
	SQLUnknown SQLClass = iota
	SQLRead
	SQLWrite
	SQLDDL
	SQLTransaction
)

// Mutates reports whether this SQL class can modify data or schema.
func (c SQLClass) Mutates() bool {
	return c == SQLWrite || c == SQLDDL || c == SQLUnknown
}

// EditKind classifies one staged row mutation.
type EditKind int

const (
	EditUpdate EditKind = iota
	EditInsert
	EditDelete
)

// ColumnValue binds one column name to a cell value.
type ColumnValue struct {
	Column string
	Value  CellValue
}

// StagedEdit describes one local, uncommitted row mutation against a table.
type StagedEdit struct {
	Kind   EditKind
	RowIdx int           // grid row index for UI highlighting (-1 for appended inserts)
	Keys   []ColumnValue // primary key predicates (required for Update/Delete)
	Values []ColumnValue // column assignments (Update/Insert)
}

// ParamStatement is a dialect-safe parameterized SQL statement ready for
// execution without string-interpolated value literals.
type ParamStatement struct {
	SQL  string
	Args []any
}

// AuditEntry records an executed or blocked database operation without ever
// storing raw credentials or DSN passwords.
type AuditEntry struct {
	Timestamp    time.Time
	ConnectionID string
	Env          Environment
	Actor        string // "user" or "mcp:<client>"
	Summary      string
	Affected     int64
	Status       string // "ok", "blocked", "error", "canceled"
}
