package app

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/runtime"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/modal"
)

type connTestOutcome struct {
	version string
	elapsed time.Duration
	err     error
}

// NewFromRuntime wires a live headless Runtime into the UI Shell.
// All database, keychain and disk I/O runs inside runtime-owned goroutines;
// View reads only the latest immutable snapshot and repaints when invalidated.
func NewFromRuntime(rt *runtime.Runtime) *Shell {
	var (
		mu          sync.Mutex
		boundSvc    bool
		svc         ui.Services
		pendingTest *connTestOutcome
		pendingErr  string
		cachedGen   uint64
		cachedSnap  Snapshot
		hasCache    bool
	)

	invalidate := func() {
		mu.Lock()
		s := svc
		bound := boundSvc
		mu.Unlock()
		if bound {
			s.Invalidate()
		}
	}

	readSnap := func() Snapshot {
		rs := rt.Snapshot()
		if hasCache && cachedGen == rs.Generation {
			return cachedSnap
		}
		cachedSnap = convertSnapshot(rs)
		cachedGen = rs.Generation
		hasCache = true
		return cachedSnap
	}

	var shell *Shell
	actions := Actions{
		SelectConnection: func(index int) {
			rt.SelectConnection(index)
		},
		SaveConnection: func(draft modal.ConnectionDraft) {
			profile := draftToProfile(draft)
			creds := runtime.Credentials{
				Password:      draft.Password,
				SSHPassword:   draft.SSHPassword,
				SSHPassphrase: draft.SSHPassphrase,
			}
			draft.ClearSecrets()
			rt.SaveAndConnect(profile, creds, func(err error) {
				mu.Lock()
				pendingErr = err.Error()
				mu.Unlock()
				invalidate()
			})
		},
		TestConnection: func(draft modal.ConnectionDraft) {
			profile := draftToProfile(draft)
			creds := runtime.Credentials{
				Password:      draft.Password,
				SSHPassword:   draft.SSHPassword,
				SSHPassphrase: draft.SSHPassphrase,
			}
			draft.ClearSecrets()
			rt.TestConnectionAsync(profile, creds, func(ver string, elapsed time.Duration, err error) {
				mu.Lock()
				pendingTest = &connTestOutcome{version: ver, elapsed: elapsed, err: err}
				mu.Unlock()
				invalidate()
			})
		},
		Refresh: func() {
			rt.Refresh()
		},
		OpenTable: func(table string) {
			if idx := rt.OpenTable(table); idx >= 0 {
				shell.activeTab = idx
			}
		},
		ApplyWhere: func(tabIdx int, where string) {
			rt.ReloadTable(tabIdx, where)
		},
		NewQuery: func() {
			if idx := rt.NewQueryTab(""); idx >= 0 {
				shell.activeTab = idx
			}
		},
		CloseTab: func(index int) {
			rt.CloseTab(index)
		},
		RunQuery: func(tabIdx int, sql string) {
			rt.RunQuery(tabIdx, sql)
		},
		CancelQuery: func(tabIdx int) {
			rt.CancelQuery(tabIdx)
		},
		ExplainQuery: func(tabIdx int, sql string) {
			rt.ExplainQuery(tabIdx, sql)
		},
		DismissError: func(tabIdx int) {
			rt.DismissError(tabIdx)
		},
		StageCellEdit: func(tabIdx, rowIdx, colIdx int, text string, null bool) {
			_ = rt.StageCellEdit(tabIdx, rowIdx, colIdx, domain.CellValue{Text: text, Null: null})
		},
		StageDeleteRow: func(tabIdx, rowIdx int) {
			_ = rt.StageDeleteRow(tabIdx, rowIdx)
		},
		DiscardStaged: func(tabIdx int) {
			rt.DiscardStaged(tabIdx)
		},
		CommitStaged: func(tabIdx int) {
			rt.CommitStagedAsync(tabIdx)
		},
		TrustHostKey: func(save bool) {
			_ = rt.TrustHostKey(save)
		},
		RejectHostKey: func() {
			rt.RejectHostKey()
		},
	}

	shell = NewWithActions(readSnap, actions)
	shell.onFrame = func(c *ui.Context) {
		mu.Lock()
		if !boundSvc {
			svc = c.Services()
			boundSvc = true
			mu.Unlock()
			rt.SetOnChange(invalidate)
			mu.Lock()
		}
		testRes := pendingTest
		pendingTest = nil
		errMsg := pendingErr
		pendingErr = ""
		mu.Unlock()

		if testRes != nil && shell.connModal.Open {
			if testRes.err != nil {
				shell.connModal.Error = testRes.err.Error()
				shell.connModal.TestSuccess = ""
			} else {
				ms := max(int(testRes.elapsed.Milliseconds()), 0)
				shell.connModal.Error = ""
				shell.connModal.TestSuccess = msgs(c).ConnTestOK(testRes.version, ms)
			}
		}
		if errMsg != "" && shell.connModal.Open {
			shell.connModal.Error = errMsg
		}
	}
	return shell
}

func draftToProfile(d modal.ConnectionDraft) domain.Profile {
	port, _ := strconv.Atoi(strings.TrimSpace(d.Port))
	sshPort, _ := strconv.Atoi(strings.TrimSpace(d.SSHPort))
	return domain.Profile{
		Name:        strings.TrimSpace(d.Name),
		Engine:      d.Engine,
		Env:         d.Env,
		ReadOnly:    d.ReadOnly,
		FilePath:    strings.TrimSpace(d.FilePath),
		Host:        strings.TrimSpace(d.Host),
		Port:        port,
		Database:    strings.TrimSpace(d.Database),
		Username:    strings.TrimSpace(d.Username),
		SSHEnabled:  d.SSHEnabled,
		SSHJumpPath: strings.TrimSpace(d.SSHJumpPath),
		SSHHost:     strings.TrimSpace(d.SSHHost),
		SSHPort:     sshPort,
		SSHUser:     strings.TrimSpace(d.SSHUser),
		SSHAuth:     convertSSHAuth(d.SSHAuth),
		SSHKeyPath:  strings.TrimSpace(d.SSHKeyPath),
	}
}

func convertSSHAuth(k modal.SSHAuthKind) domain.SSHAuthKind {
	switch k {
	case modal.SSHAuthPrivateKey:
		return domain.SSHAuthKey
	case modal.SSHAuthPassword:
		return domain.SSHAuthPassword
	default:
		return domain.SSHAuthAgent
	}
}

func displayVia(p domain.Profile) string {
	if !p.SSHEnabled {
		return ""
	}
	if p.SSHJumpPath != "" {
		return p.SSHJumpPath
	}
	return p.SSHHost
}

func convertSnapshot(rs runtime.Snapshot) Snapshot {
	conns := make([]Connection, len(rs.Connections))
	for i, rc := range rs.Connections {
		objs := make([]Object, len(rc.Objects))
		for j, o := range rc.Objects {
			objs[j] = Object{
				Name: o.Name,
				Kind: convertObjectKind(o.Kind),
				Rows: o.Rows,
			}
		}
		conns[i] = Connection{
			Name:     rc.Profile.Name,
			Engine:   rc.Profile.Engine,
			Version:  rc.Version,
			Address:  rc.Profile.DisplayAddress(),
			Via:      displayVia(rc.Profile),
			Env:      rc.Profile.Env,
			ReadOnly: rc.Profile.ReadOnly,
			Objects:  objs,
		}
	}

	tabs := make([]Tab, len(rs.Tabs))
	for i, rtTab := range rs.Tabs {
		cols := make([]Column, len(rtTab.Columns))
		for j, col := range rtTab.Columns {
			cols[j] = Column{
				Name:    col.Name,
				Type:    col.Type,
				Numeric: col.Numeric,
				Key:     col.PrimaryKey,
			}
		}
		rows := make([]Row, len(rtTab.Rows))
		for j, r := range rtTab.Rows {
			cells := make([]Cell, len(r.Cells))
			for k, cell := range r.Cells {
				cells[k] = Cell{
					Text:   cell.Text,
					Null:   cell.Null,
					Staged: cell.Staged,
				}
			}
			rows[j] = Row{
				Cells:  cells,
				Change: convertRowChange(r.Change),
			}
		}
		kind := TabTable
		if rtTab.Kind == runtime.TabQuery {
			kind = TabQuery
		}
		tabs[i] = Tab{
			ID:        rtTab.ID,
			Kind:      kind,
			Title:     rtTab.Title,
			Where:     rtTab.Where,
			Query:     rtTab.Query,
			Columns:   cols,
			Rows:      rows,
			Total:     rtTab.Total,
			Elapsed:   rtTab.Elapsed,
			Running:   rtTab.Running,
			Error:     rtTab.Error,
			Pending:   Pending{Modified: rtTab.Pending.Modified, Inserted: rtTab.Pending.Inserted, Deleted: rtTab.Pending.Deleted},
			StagedSQL: rtTab.StagedSQL,
		}
	}

	return Snapshot{
		Generation:     rs.Generation,
		Connections:    conns,
		Active:         rs.Active,
		Tabs:           tabs,
		PendingHostKey: rs.PendingHostKey,
	}
}

func convertObjectKind(k domain.ObjectKind) ObjectKind {
	switch k {
	case domain.ObjectView:
		return ObjectView
	case domain.ObjectFunction:
		return ObjectFunction
	default:
		return ObjectTable
	}
}

func convertRowChange(c runtime.RowChange) Change {
	switch c {
	case runtime.RowModified:
		return Modified
	case runtime.RowInserted:
		return Inserted
	case runtime.RowDeleted:
		return Deleted
	default:
		return Unchanged
	}
}
