# KyanDB Architecture

This document defines the package layout, dependency boundaries, runtime ownership model, and safety invariants for KyanDB. All architectural boundaries described here are enforced mechanically by [`arch_test.go`](../arch_test.go) during `go test ./...`.

## Design Goals

1. **Pure Go, Zero CGO**: Every build target (macOS, Windows, Linux) compiles with `CGO_ENABLED=0`. No C toolchains, no libpq/libsqlite3 C bindings, no Electron, no JVM.
2. **Strict UI / Core Separation**: Database drivers, SSH tunneling, SQL safety checks, persistence, and MCP workflows must work cleanly headlessly without importing `github.com/egoist/mygo/ui`.
3. **Production-Safe by Construction**: Read-only locks, environment guardrails (`dev` / `staging` / `prod`), staged-edit SQL previews, and MCP write approvals are enforced in one central runtime layer—never solely in UI controls.
4. **Deterministic Resource Ownership**: Every database pool, SSH jump tunnel, query goroutine, and MCP lease has one explicit owner, a `context.Context` cancellation path, and deterministic teardown.

---

## Package Layout

Packages under `internal/` are created incrementally as their milestone lands—do not pre-create empty placeholder packages.

```text
kyandb/
├── main.go                    # Entrypoint only: boot mygo.App, wire runtime & root window
├── arch_test.go               # Mechanical import-boundary & zero-CGO enforcement
├── mygo.json                  # App metadata, bundle ID, icon, packaging config
├── assets/                    # Canonical icon.svg, icon.png, icon.icns
├── locales/                   # ICU message catalogs (en source, zh-Hans) + generated accessors; imported only by internal/ui
├── .interface-design/
│   └── system.md              # Visual system: tokens, metrics, component patterns
├── docs/
│   └── ARCHITECTURE.md        # This document
└── internal/
    ├── domain/                # Pure value types & sentinel errors (stdlib only)
    ├── secret/                # Redacted/zeroizable secret wrappers + KeychainStore (mygo.Secrets)
    ├── config/                # Non-sensitive JSON persistence (profiles, prefs, query history)
    ├── tunnel/                # Pure-Go SSH & multi-hop ProxyJump tunnel manager
    ├── engine/                # Extensible engine contract + pure-Go database drivers
    │   ├── postgres/          #   pgx (M1)
    │   ├── mysql/             #   go-sql-driver/mysql (M1)
    │   └── sqlite/            #   modernc.org/sqlite (M1)
    ├── sqlguard/              # SQL read/write/DDL classifier, identifier quoting, staged SQL diff
    ├── runtime/               # ConnectionManager, SessionRegistry, QueryRunner, AuditLog, SafetyGate
    ├── mcp/                   # Local loopback MCP server, connection-scoped TTL leases, approval queue
    └── ui/                    # All mygo/ui views & interaction state (ONLY layer importing mygo/ui)
        ├── app/               #   Root window shell, workspace tabs, split layout, shortcuts
        ├── sidebar/           #   Connection tree, schema browser, environment chrome badges
        ├── grid/              #   Virtualized result/table grid, cell drawer, staged-edit diff bar
        ├── editor/            #   SQL editor tabs, execution toolbar, EXPLAIN view, history
        ├── modal/             #   Connection form, SSH tunnel config, SQL diff & MCP approval modals
        └── theme/             #   Theme tokens, dev/staging/prod accent colors, shared UI helpers
```

---

## Dependency Graph (Downward-Only DAG)

```text
main.go
  ├── internal/runtime
  ├── internal/config
  ├── internal/secret
  └── internal/ui/app
        ├── internal/ui/{sidebar,grid,editor,modal,theme}
        ├── internal/runtime    (narrow controller methods + immutable snapshots)
        └── internal/domain

internal/mcp
  ├── internal/runtime          (shares the exact same SafetyGate, Registry, and AuditLog)
  ├── internal/sqlguard
  └── internal/domain

internal/runtime
  ├── internal/domain
  ├── internal/secret
  ├── internal/config
  ├── internal/tunnel
  ├── internal/engine/...
  └── internal/sqlguard

internal/engine/...  ──▶ internal/domain, internal/tunnel, internal/secret
internal/sqlguard    ──▶ internal/domain
internal/tunnel      ──▶ internal/domain, internal/secret
internal/config      ──▶ internal/domain
internal/secret      ──▶ (stdlib + narrow mygo.Secrets adapter)
internal/domain      ──▶ (stdlib ONLY)
```

### Enforced Import Rules (`arch_test.go`)

| Package Scope | Forbidden Imports |
| --- | --- |
| **Entire repository** | `"C"` (CGO), `github.com/mattn/go-sqlite3` |
| **`internal/domain`** | Any third-party package, any other `internal/...` package |
| **Non-UI `internal/...`** (all except `internal/ui/...`) | `github.com/egoist/mygo/ui`, `locales` |
| **`internal/ui/...`** | `database/sql`, `internal/engine/...`, `internal/tunnel`, `internal/config`, `internal/secret` |
| **`internal/config`** | `internal/secret`, `internal/engine/...`, `internal/tunnel`, `internal/runtime` |
| **`internal/sqlguard`** | `database/sql`, `internal/engine/...`, `internal/tunnel`, `internal/secret`, `internal/runtime` |
| **`internal/mcp`** | `database/sql`, `internal/engine/...`, `internal/tunnel`, `internal/secret`, `internal/config` |

---

## Runtime Ownership & Lifecycle Model

### 1. Single Owner for Live Connections and Tunnels

`internal/runtime` owns all active database handles (`engine.Conn`), SSH tunnel chains (`tunnel.Chain`), running queries, and MCP leases.

- **UI views hold opaque IDs (`domain.ConnectionID`, `domain.QueryID`)**, never `*sql.DB`, `*pgxpool.Pool`, or `*ssh.Client`.
- **Closing a tab** cancels only that tab's `QueryID` context. Shared database connections and SSH tunnels remain alive until explicitly disconnected or app shutdown.
- **Disconnecting a connection** follows strict reverse teardown:
  1. Cancel all in-flight query contexts bound to that `ConnectionID`.
  2. Revoke active MCP leases targeting that `ConnectionID`.
  3. Close the database engine pool.
  4. Close the leaf SSH tunnel and decrement reference counts on parent jump hosts; tear down any jump host whose refcount reaches zero.

### 2. Non-Blocking UI Rendering (`mygo/ui`)

`mygo/ui` renders inside a GPU frame loop. Therefore:

- `view(c *ui.Context)` reads only in-memory UI state and immutable snapshots published by `internal/runtime`.
- User actions (connect, run query, introspect schema, commit staged edits, export CSV) dispatch asynchronous commands to `internal/runtime` with a cancellable `context.Context`.
- Background workers post typed results/events back to the UI state store and request a repaint—never performing network, database, keychain, or filesystem I/O inline inside `view`.

---

## Safety & Staged-Edit Pipeline

Every SQL execution path—SQL editor, inline grid edits, and MCP tool calls—passes through `internal/sqlguard` and `internal/runtime`'s `SafetyGate`:

1. **Classification (`sqlguard`)**: Parse/classify statement intent (`Read`, `Write`, `DDL`, `Admin`, `Unknown`). Ambiguous or unrecognized statements default to non-read (fail closed).
2. **Policy Check (`runtime.SafetyGate`)**:
   - If the connection is `ReadOnly` (or an MCP lease is read-only), reject non-`Read` statements before touching the driver.
   - If the connection is tagged `prod`, require explicit confirmation / approval token for `Write` and `DDL`.
   - When the database driver supports session-level read-only enforcement, configure it at connect/transaction time as defense-in-depth.
3. **Staged Inline Edits**:
   - Grid edits accumulate as `domain.StagedEdit` drafts keyed by table + primary key.
   - `internal/sqlguard` generates dialect-quoted, parameterized SQL previews so the user inspects the exact statements before committing inside a single transaction.
4. **Audit Trail**:
   - Executed mutations, DDL, and MCP operations emit sanitized `domain.AuditEvent` records (timestamp, connection ID/env tag, actor `user` vs `mcp:<client>`, statement template/summary, affected rows, status)—never raw credentials.

---

## Secrets & Credential Isolation

- **Storage**: `internal/config` writes non-sensitive connection metadata (host, port, username, env tag, tunnel topology, opaque `SecretRef`) to user config JSON. Actual passwords, SSH passphrases, private key blobs, and tokens live exclusively in `internal/secret`, backed by `mygo.Secrets` (macOS Keychain, Windows Credential Manager, Linux Secret Service).
- **Redaction**: Secret wrapper types (`secret.String`, `secret.Bytes`) implement `fmt.Stringer`, `fmt.GoStringer`, `encoding.TextMarshaler`, and `json.Marshaler` returning `[REDACTED]`.
- **Zeroization**: Ephemeral credential buffers are wiped (`clear(buf)`) once consumed by driver dialers or SSH signers.
- **No Secret Leakage**: Error wrappers must scrub DSN passwords before formatting errors surfaced to UI toasts, logs, or MCP clients.

---

## External AI & MCP Bridge (Milestone 3)

- Runs strictly on local loopback (`127.0.0.1` / `::1`) inside `internal/mcp`.
- Calls into `internal/runtime`—never opens its own database connections or reads raw keychain secrets.
- Every session requires an explicit user-granted **connection-scoped lease** with a bounded TTL and permission level (`schema-only`, `read-only`, or `read+approval-gated-write`).
- Any write or DDL request pauses on an in-app approval gate displaying the target connection, environment badge, and exact SQL diff before execution.

---

## Testing Strategy

- **`arch_test.go`**: Runs on every `go test ./...` to enforce layer isolation and zero-CGO rules.
- **Unit & Contract Tests**: Table-driven tests in `sqlguard`, `config`, `secret`, `tunnel`, and `runtime` (with in-memory fakes for keychain/drivers and `-race` enabled).
- **Headless UI Tests**: Use `mygo/ui.NewTester` to verify interaction flows (selection, filtering, staged-edit diff workflow, modal confirmation) deterministically without requiring a display server.
- **Live Engine Integration Tests**: Opt-in behind build tags or env vars (e.g., `KYANDB_TEST_PG_DSN`) so default `go test ./...` stays fast, offline, and hermetic.
