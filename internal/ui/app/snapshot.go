package app

import (
	"time"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/modal"
)

// Snapshot is the immutable view of workspace state the shell renders.
// internal/runtime will produce it; the shell never mutates it, and every
// aggregate (counts, totals) arrives precomputed so a frame stays O(visible).
type Snapshot struct {
	// Generation changes whenever any field changes, letting views keep
	// derived indexes until then.
	Generation     uint64
	Connections    []Connection
	Active         int // index into Connections, or -1
	Tabs           []Tab
	PendingHostKey *domain.HostKeyChallenge
}

// Connection is a saved connection as the chrome shows it. Address and Via
// are display strings without credentials.
type Connection struct {
	Name     string
	Engine   domain.Engine
	Version  string
	Address  string
	Via      string // jump path, empty for direct connections
	Env      domain.Environment
	ReadOnly bool
	Objects  []Object
}

// ObjectKind groups schema objects in the sidebar.
type ObjectKind int

const (
	ObjectTable ObjectKind = iota
	ObjectView
	ObjectFunction
)

// Object is one browsable schema object. Rows is an estimate; negative
// means unknown.
type Object struct {
	Name string
	Kind ObjectKind
	Rows int64
}

// TabKind selects the layout of a workspace tab.
type TabKind int

const (
	TabTable TabKind = iota
	TabQuery
)

// Tab is one open workspace tab.
type Tab struct {
	ID        uint64 // stable runtime tab identifier (0 for static fixtures)
	Kind      TabKind
	Title     string
	Where     string
	Query     string
	Columns   []Column
	Rows      []Row
	Total     int64         // rows matching, which may exceed len(Rows)
	Elapsed   time.Duration // zero until the tab has run
	Running   bool          // true while a query or reload is in flight
	Error     string        // non-empty inline error summary (never contains credentials)
	Pending   Pending
	StagedSQL string // generated transaction SQL for Review SQL preview
}

// Pending summarizes staged edits in a tab.
type Pending struct {
	Modified, Inserted, Deleted int
}

// Count returns the number of staged row changes.
func (p Pending) Count() int { return p.Modified + p.Inserted + p.Deleted }

// Column describes one result column.
type Column struct {
	Name    string
	Type    string
	Numeric bool
	Key     bool
}

// Change marks a row's staged state.
type Change int

const (
	Unchanged Change = iota
	Modified
	Inserted
	Deleted
)

// Row is one result row and its staged state.
type Row struct {
	Cells  []Cell
	Change Change
}

// Cell is one result value; Null is distinct from an empty string, and
// Staged marks a value edited locally but not yet committed.
type Cell struct {
	Text   string
	Null   bool
	Staged bool
}

// Actions receives user intent from the shell. Every field is optional;
// internal/runtime wires these when attached.
type Actions struct {
	SelectConnection func(index int)
	NewConnection    func()
	SaveConnection   func(draft modal.ConnectionDraft)
	TestConnection   func(draft modal.ConnectionDraft)
	Refresh          func()
	OpenTable        func(table string)
	ApplyWhere       func(tabIndex int, where string)
	NewQuery         func()
	CloseTab         func(index int)
	RunQuery         func(tabIndex int, sql string)
	CancelQuery      func(tabIndex int)
	ExplainQuery     func(tabIndex int, sql string)
	DismissError     func(tabIndex int)
	StageCellEdit    func(tabIndex, rowIndex, colIndex int, text string, null bool)
	StageDeleteRow   func(tabIndex, rowIndex int)
	DiscardStaged    func(tabIndex int)
	ReviewSQL        func(tabIndex int)
	CommitStaged     func(tabIndex int)
	TrustHostKey     func(save bool)
	RejectHostKey    func()
}
