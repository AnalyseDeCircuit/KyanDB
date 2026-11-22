// Package app is the root window shell: quiet native titlebar, unified
// sidebar/tab datum, schema explorer, workspace grid/editor, staged-edit
// ledger and status bar.
package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/AnalyseDeCircuit/kyandb/internal/ui/modal"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

// Shell holds view state only (selection, split sizes, scroll, filters,
// popovers and open modals); workspace data comes from the snapshot function
// on every frame.
type Shell struct {
	snapshot func() Snapshot
	actions  Actions
	onFrame  func(c *ui.Context)

	hideSidebar  bool
	connMenuOpen bool
	connModal    modal.ConnectionState
	reviewModal  modal.ReviewSQLState
	reviewTabIdx int
	sidebarW     float32
	objects      objectIndex
	activeTab    int
	editorPct    float32
	tabs         map[uint64]*tabView
}

// tabView is per-tab view state, kept apart so switching tabs preserves
// each grid's scroll, selection, inline cell editor and SQL buffer.
type tabView struct {
	grid        ui.ListState
	rows        ui.Selection[int]
	where       string
	whereSynced string
	editor      *editor.State

	editActive bool
	editFresh  bool
	editRow    int
	editCol    int
	editDraft  string
	editOrig   string
	editOrigN  bool
}

// New returns a shell reading state through snapshot. The function runs on
// the render path and must return a cached value without doing I/O.
func New(snapshot func() Snapshot) *Shell {
	return NewWithActions(snapshot, Actions{})
}

// NewWithActions returns a shell wired to runtime actions.
func NewWithActions(snapshot func() Snapshot, actions Actions) *Shell {
	s := &Shell{
		snapshot:  snapshot,
		actions:   actions,
		sidebarW:  theme.SidebarW,
		editorPct: 34,
		tabs:      map[uint64]*tabView{},
	}
	s.objects.init()
	return s
}

// View renders one frame.
func (s *Shell) View(c *ui.Context) {
	if s.onFrame != nil {
		s.onFrame(c)
	}
	k := theme.For(c.Theme().Dark)
	c.SetTheme(k.UI)
	snap := s.snapshot()
	if len(snap.Tabs) == 0 {
		s.activeTab = 0
	} else {
		s.activeTab = min(max(s.activeTab, 0), len(snap.Tabs)-1)
	}
	conn := snap.active()

	s.handleShortcuts(c, snap)

	ui.Column(c).Fill().Children(func() {
		s.titleBar(c, k, conn)
		ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
			if s.hideSidebar {
				s.workspace(c, k, snap, conn)
				return
			}
			ui.Split(c, &s.sidebarW,
				func() { s.sidebar(c, k, snap, conn) },
				func() { s.workspace(c, k, snap, conn) },
			).Fill()
		})
		s.statusBar(c, k.P, snap, conn)
	})

	modal.Connection(c, k, &s.connModal, modal.ConnectionCallbacks{
		OnSave: s.actions.SaveConnection,
		OnTest: s.actions.TestConnection,
	})
	modal.ReviewSQL(c, k, &s.reviewModal, modal.ReviewSQLCallbacks{
		OnCommit: func() {
			if s.actions.CommitStaged != nil {
				s.actions.CommitStaged(s.reviewTabIdx)
			}
		},
		OnDiscard: func() {
			if s.actions.DiscardStaged != nil {
				s.actions.DiscardStaged(s.reviewTabIdx)
			}
		},
	})
	modal.HostKey(c, k, snap.PendingHostKey, modal.HostKeyCallbacks{
		OnTrust:  s.actions.TrustHostKey,
		OnReject: s.actions.RejectHostKey,
	})
}

func (s *Shell) openNewConnection() {
	s.connModal.OpenNew()
	if s.actions.NewConnection != nil {
		s.actions.NewConnection()
	}
}

func (s *Shell) handleShortcuts(c *ui.Context, snap Snapshot) {
	if c.Shortcut(ui.Cmd, ui.KeyB) && !c.ShortcutRepeated() {
		s.hideSidebar = !s.hideSidebar
	}
	if c.Shortcut(ui.Cmd, ui.KeyK) && !c.ShortcutRepeated() {
		s.connMenuOpen = !s.connMenuOpen
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) && !c.ShortcutRepeated() {
		s.openNewConnection()
	}
	if c.Shortcut(ui.Cmd, ui.KeyR) && !c.ShortcutRepeated() && s.actions.Refresh != nil {
		s.actions.Refresh()
	}
	if c.Shortcut(ui.Cmd, ui.KeyT) && !c.ShortcutRepeated() && s.actions.NewQuery != nil {
		s.actions.NewQuery()
	}
	if c.Shortcut(ui.Cmd, ui.KeyW) && !c.ShortcutRepeated() && len(snap.Tabs) > 0 && s.actions.CloseTab != nil {
		s.actions.CloseTab(min(s.activeTab, len(snap.Tabs)-1))
	}
	if c.Shortcut(ui.Cmd, ui.KeyEnter) && !c.ShortcutRepeated() && len(snap.Tabs) > 0 {
		idx := min(s.activeTab, len(snap.Tabs)-1)
		tab := snap.Tabs[idx]
		if tab.Kind == TabQuery && !tab.Running && s.actions.RunQuery != nil {
			s.actions.RunQuery(idx, s.currentQuerySQL(idx, tab))
		}
	}
}

func (s *Shell) currentQuerySQL(tabIdx int, tab Tab) string {
	if v := s.tabs[tabKey(tabIdx, tab.ID)]; v != nil && v.editor != nil && v.editor.Buffer != nil {
		return v.editor.Buffer.String()
	}
	return tab.Query
}

func tabKey(i int, id uint64) uint64 {
	if id != 0 {
		return id
	}
	return uint64(i) | (1 << 62)
}

func (snap Snapshot) active() *Connection {
	if snap.Active < 0 || snap.Active >= len(snap.Connections) {
		return nil
	}
	return &snap.Connections[snap.Active]
}

func (s *Shell) tabView(i int, id uint64) *tabView {
	key := tabKey(i, id)
	v := s.tabs[key]
	if v == nil {
		v = &tabView{}
		// CellSelect needs a multi-row Selection; a grid chooses blocks of
		// cells like a spreadsheet, not a single row.
		v.grid.Selection = &v.rows
		v.grid.CellSelect = true
		v.grid.RowHeight = theme.RowHeight
		s.tabs[key] = v
	}
	return v
}

func seam(c *ui.Context, color ui.Color) {
	ui.Box(c).Height(1).FillWidth().Background(color)
}

// titleBar is a slim native macOS drag strip: traffic lights on the left,
// quiet plain-text connection context centered (no browser-style pill), and
// unobtrusive window-level toggles on the right. Protected environments mark
// the top edge with a restrained 2 DIP strip.
func (s *Shell) titleBar(c *ui.Context, k theme.Set, conn *Connection) {
	p := k.P
	bar := c.TitleBar()
	if conn != nil && conn.Env.Protected() {
		ui.Box(c).Height(2).FillWidth().Background(p.Env(conn.Env).Alpha(0.85))
	}
	ui.Row(c).Height(max(bar.Height, theme.TitleBarHeight)).
		Padding(0, max(bar.Right, 8), 0, max(bar.Left+6, 8)).
		AlignItems(ui.Center).Background(p.Matrix).DragWindow().Children(func() {
		ui.Row(c).Width(72).AlignItems(ui.Center).Children(func() {
			if iconButton(c, k, "toggle-sidebar", theme.IconSidebar,
				msgs(c).ToolbarToggleSidebar(),
				withShortcut(msgs(c).ToolbarToggleSidebar(), cmdShortcut("B", "B"))) {
				s.hideSidebar = !s.hideSidebar
			}
		})
		ui.Spacer(c)
		if conn == nil {
			ui.Text(c, "KyanDB").FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkFaint)
		} else {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				env := p.Env(conn.Env)
				ui.Box(c).Size(6, 6).Radius(3).Background(env)
				if conn.Env.Protected() {
					ui.Text(c, msgs(c).EnvTag(string(conn.Env))).FontSize(10).FontWeight(700).LetterSpacing(0.4).TextColor(env)
				}
				ui.Text(c, conn.Name).FontSize(theme.SmallSize).FontWeight(600).TextColor(p.InkSoft).SingleLine()
				ui.Text(c, "·").FontSize(theme.SmallSize).TextColor(p.InkGhost)
				ui.Text(c, engineLabel(conn)).FontSize(theme.SmallSize).TextColor(p.InkFaint).SingleLine()
				route := conn.Address
				if conn.Via != "" {
					route = msgs(c).PlateVia(conn.Via)
				}
				if route != "" {
					ui.Text(c, "·").FontSize(theme.SmallSize).TextColor(p.InkGhost)
					ui.Text(c, route).Font(theme.MonoFamily).FontSize(theme.SmallSize).TextColor(p.InkFaint).SingleLine().Ellipsis("…").MaxWidth(360)
				}
				if conn.ReadOnly {
					ui.Text(c, "·").FontSize(theme.SmallSize).TextColor(p.InkGhost)
					ui.Row(c).Gap(3).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, theme.IconLock).Size(10, 10).TextColor(p.InkFaint)
						ui.Text(c, msgs(c).PlateReadOnly()).FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkFaint).SingleLine()
					})
				}
			})
		}
		ui.Spacer(c)
		ui.Row(c).Width(72).Gap(2).Justify(ui.End).AlignItems(ui.Center).Children(func() {
			if iconButton(c, k, "refresh", theme.IconRefresh,
				msgs(c).ToolbarRefresh(),
				withShortcut(msgs(c).ToolbarRefresh(), cmdShortcut("R", "R"))) && s.actions.Refresh != nil {
				s.actions.Refresh()
			}
			if iconButton(c, k, "new-query", theme.IconPlus,
				msgs(c).ToolbarNewQuery(),
				withShortcut(msgs(c).ToolbarNewQuery(), cmdShortcut("T", "T"))) && s.actions.NewQuery != nil {
				s.actions.NewQuery()
			}
		})
	})
	seam(c, p.Seam)
}

// connectionPopover anchors the compact switcher cleanly under the sidebar's
// connection header so it never floats across the middle of the data grid.
func (s *Shell) connectionPopover(c *ui.Context, k theme.Set, anchor ui.Element, snap Snapshot) {
	p := k.P
	ui.PopoverBase(c, anchor, &s.connMenuOpen, func(panel ui.Element) {
		panel.Width(320).Margin(4, 0, 0, 0).Radius(k.R.LG).
			Background(p.Matrix).Border(1, p.SeamStrong).
			Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.24)).
			TransformOrigin(0, 0).Transition(theme.PopoverTransition).
			Clip().Children(func() {
			if len(snap.Connections) == 0 {
				ui.Row(c).Padding(12).Justify(ui.Center).Children(func() {
					ui.Text(c, msgs(c).SidebarNoConnections()).TextColor(p.InkFaint)
				})
			} else {
				ui.Column(c).Padding(4).Gap(2).Children(func() {
					for i, item := range snap.Connections {
						active := i == snap.Active
						row := ui.ButtonBase(c.Key(fmt.Sprintf("switch-conn-%d", i))).
							Height(34).PaddingX(10).Gap(8).Radius(k.R.SM).
							AlignItems(ui.Center).FillWidth()
						switch {
						case row.Hovered():
							row.Background(p.Ink.Alpha(0.05))
						case active:
							row.Background(p.KyaniteWash)
						}
						row.Children(func() {
							env := p.Env(item.Env)
							ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(env)
							ui.Column(c).Grow(1).MinWidth(0).Children(func() {
								ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
									ui.Text(c, item.Name).FontWeight(600).TextColor(p.Ink).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
									ui.Text(c, string(item.Engine)).FontSize(10.5).TextColor(p.InkFaint).SingleLine().Shrink(0)
								})
								route := item.Address
								if item.Via != "" {
									route = msgs(c).PlateVia(item.Via)
								}
								ui.Text(c, route).Font(theme.MonoFamily).FontSize(10.5).TextColor(p.InkGhost).SingleLine().Ellipsis("…")
							})
							if active {
								ui.Icon(c, theme.IconCheck).Size(12, 12).Shrink(0).TextColor(p.KyaniteInk).Label(msgs(c).PlateCurrent())
							}
						})
						if row.Clicked() {
							s.connMenuOpen = false
							if s.actions.SelectConnection != nil {
								s.actions.SelectConnection(i)
							}
						}
					}
				})
			}
			seam(c, p.Seam)
			ui.Row(c).Padding(4).Children(func() {
				add := ui.ButtonBase(c.Key("popover-new-conn")).
					Height(28).PaddingX(10).Gap(6).Radius(k.R.SM).
					AlignItems(ui.Center).Justify(ui.Start).FillWidth()
				if add.Hovered() {
					add.Background(p.Ink.Alpha(0.05))
				}
				add.Children(func() {
					ui.Icon(c, theme.IconPlus).Size(12, 12).TextColor(p.InkSoft)
					ui.Text(c, msgs(c).SidebarNewConnection()).FontWeight(500).TextColor(p.Ink).SingleLine()
				})
				if add.Clicked() {
					s.connMenuOpen = false
					s.openNewConnection()
				}
			})
		})
	})
}

// iconButton is a borderless toolbar button with smooth hover tint and tactile press feedback.
func iconButton(c *ui.Context, k theme.Set, id string, icon *ui.SVG, label, tooltip string) bool {
	p := k.P
	b := ui.ButtonBase(c.Key(id)).Size(24, 22).Radius(k.R.SM).Label(label).Tooltip(tooltip)
	bg, color := ui.Transparent, p.InkFaint
	if b.Hovered() {
		bg, color = p.Ink.Alpha(0.06), p.Ink
	}
	b.Background(b.AnimateColor("bg", bg, theme.HoverColor))
	theme.ApplyPress(b, b.Pressed())
	b.Children(func() {
		ui.Icon(c, icon).Size(14, 14).TextColor(color)
	})
	return b.Clicked()
}

type buttonKind int

const (
	buttonGhost buttonKind = iota
	buttonOutline
	buttonPrimary
	buttonDanger
)

// button is the compact 24 DIP action button used in workspace bars.
func button(c *ui.Context, k theme.Set, id, label string, kind buttonKind, icon *ui.SVG) bool {
	p := k.P
	b := ui.ButtonBase(c.Key(id)).Height(24).PaddingX(10).Gap(5).Radius(k.R.SM)
	hover, pressed := b.Hovered(), b.Pressed()
	var bg, fg ui.Color
	switch kind {
	case buttonPrimary:
		bg, fg = p.Kyanite, theme.OnKyanite
		if hover {
			bg = p.Kyanite.Mix(ui.Hex("#ffffff"), 0.10)
		}
	case buttonDanger:
		bg, fg = p.OxideFill, theme.OnDanger
		if hover {
			bg = p.OxideFill.Mix(ui.Hex("#ffffff"), 0.10)
		}
	case buttonOutline:
		bg, fg = ui.Transparent, p.Ink
		b.Border(1, p.SeamStrong)
		if hover {
			bg = p.Ink.Alpha(0.05)
		}
	default:
		bg, fg = ui.Transparent, p.InkSoft
		if hover {
			bg, fg = p.Ink.Alpha(0.05), p.Ink
		}
	}
	if pressed {
		bg = bg.Mix(ui.Hex("#000000"), 0.08)
	}
	b.Background(b.AnimateColor("bg", bg, theme.HoverColor))
	theme.ApplyPress(b, pressed)
	b.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(11, 11).TextColor(fg)
		}
		ui.Text(c, label).FontWeight(500).TextColor(fg).SingleLine()
	})
	return b.Clicked()
}

// statusBar mirrors OxideTerm's quiet bottom bar: connection state on the
// left, row count and elapsed time on the right.
func (s *Shell) statusBar(c *ui.Context, p *theme.Palette, snap Snapshot, conn *Connection) {
	seam(c, p.Seam)
	ui.Row(c).Height(24).PaddingX(10).Gap(12).AlignItems(ui.Center).Background(p.Matrix).Children(func() {
		if conn != nil {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(p.Env(conn.Env))
				ui.Text(c, conn.Name).FontSize(theme.SmallSize).TextColor(p.InkFaint).SingleLine()
				if conn.ReadOnly {
					ui.Text(c, msgs(c).PlateReadOnly()).FontSize(theme.SmallSize).TextColor(p.InkGhost).SingleLine()
				}
			})
		}
		ui.Spacer(c)
		if len(snap.Tabs) > 0 {
			tab := snap.Tabs[min(max(s.activeTab, 0), len(snap.Tabs)-1)]
			meta := func(text string) {
				ui.Text(c, text).FontSize(theme.SmallSize).FontFeatures("tnum").TextColor(p.InkFaint).SingleLine()
			}
			meta(rowsLabel(c, len(tab.Rows), tab.Total))
			if tab.Elapsed > 0 {
				meta(formatElapsed(c, tab.Elapsed))
			}
		}
	})
}
