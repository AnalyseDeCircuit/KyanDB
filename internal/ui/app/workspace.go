package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/AnalyseDeCircuit/kyandb/internal/ui/modal"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

func (s *Shell) workspace(c *ui.Context, k theme.Set, snap Snapshot, conn *Connection) {
	p := k.P
	ui.Column(c).Fill().Background(p.Canvas).Children(func() {
		if len(snap.Tabs) == 0 {
			s.welcome(c, k, snap)
			return
		}
		active := min(max(s.activeTab, 0), len(snap.Tabs)-1)
		prevActive := active
		s.tabStrip(c, k, snap.Tabs, &active)
		if active != prevActive {
			s.activeTab = active
		}

		tab := snap.Tabs[active]
		v := s.tabView(active, tab.ID)
		if tab.Where != v.whereSynced {
			v.where = tab.Where
			v.whereSynced = tab.Where
		}
		if v.editActive && (v.editRow >= len(tab.Rows) || v.editCol >= len(tab.Columns)) {
			v.editActive = false
		}
		switch tab.Kind {
		case TabQuery:
			s.queryBar(c, k, active, tab)
			ui.SplitVerticalPercent(c, &s.editorPct,
				func() { queryEditor(c, k, v, tab) },
				func() {
					ui.Column(c).Fill().Children(func() {
						seam(c, p.Seam)
						if tab.Error != "" {
							s.errorBanner(c, k, active, tab)
						}
						s.gridOrEmpty(c, k, active, v, tab, conn)
					})
				},
			).Grow(1)
		default:
			s.whereBar(c, k, active, v)
			if tab.Error != "" {
				s.errorBanner(c, k, active, tab)
			}
			ui.Column(c).Grow(1).Children(func() { s.gridOrEmpty(c, k, active, v, tab, conn) })
		}
		if tab.Pending.Count() > 0 {
			s.ledger(c, k, active, tab, conn)
		}
	})
}

// welcome mirrors OxideTerm's restrained two-column desktop start page,
// adapted for database connections: brand mark, saved connections on the
// left, primary actions on the right, and a quiet shortcut strip below.
func (s *Shell) welcome(c *ui.Context, k theme.Set, snap Snapshot) {
	p := k.P
	m := msgs(c)
	ui.Column(c).Fill().Center().Padding(32).Children(func() {
		ui.Column(c).Width(620).MaxWidthPercent(92).Gap(18).Children(func() {
			// Wordmark header
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, theme.LogoMark).Size(22, 26).TextColor(p.Kyanite)
				ui.Text(c, "KyanDB").FontSize(20).FontWeight(600).LetterSpacing(-0.3).TextColor(p.Ink)
			})
			seam(c, p.Seam)

			// Two-column body
			ui.Row(c).Gap(24).AlignItems(ui.Start).Children(func() {
				// Left column: saved connections
				ui.Column(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, m.WelcomeRecent()).FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkFaint)
						ui.Spacer(c)
						if len(snap.Connections) > 0 {
							ui.Text(c, formatInt(c, int64(len(snap.Connections)))).FontSize(theme.SmallSize).FontFeatures("tnum").TextColor(p.InkGhost)
						}
					})
					if len(snap.Connections) == 0 {
						ui.Row(c).Height(64).Center().Radius(k.R.MD).Border(1, p.Seam).Children(func() {
							ui.Text(c, m.WorkspaceNoTabs()).TextColor(p.InkFaint)
						})
					} else {
						ui.Column(c).Radius(k.R.MD).Border(1, p.Seam).Clip().Children(func() {
							for i, conn := range snap.Connections {
								if i > 0 {
									seam(c, p.Seam)
								}
								row := ui.ButtonBase(c.Key(fmt.Sprintf("quick-%d", i))).
									Height(44).PaddingX(12).Gap(10).AlignItems(ui.Center).FillWidth()
								if row.Hovered() {
									row.Background(p.Ink.Alpha(0.035))
								}
								row.Children(func() {
									ui.Box(c).Size(7, 7).Radius(3.5).Shrink(0).Background(p.Env(conn.Env))
									ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
										ui.Text(c, conn.Name).FontWeight(500).TextColor(p.Ink).SingleLine().Ellipsis("…")
										route := conn.Address
										if conn.Via != "" {
											route = m.PlateVia(conn.Via)
										}
										ui.Text(c, route).Font(theme.MonoFamily).FontSize(theme.SmallSize).TextColor(p.InkFaint).SingleLine().Ellipsis("…")
									})
									ui.Text(c, string(conn.Engine)).FontSize(10.5).TextColor(p.InkGhost).SingleLine().Shrink(0)
									ui.Icon(c, theme.IconChevron).Size(12, 12).Shrink(0).TextColor(p.InkGhost)
								})
								if row.Clicked() && s.actions.SelectConnection != nil {
									s.actions.SelectConnection(i)
								}
							}
						})
					}
				})

				// Right column: primary actions
				ui.Column(c).Width(220).Gap(8).Children(func() {
					ui.Text(c, m.WelcomeStart()).FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkFaint)
					newBtn := ui.ButtonBase(c.Key("welcome-new-conn")).
						Height(44).PaddingX(12).Gap(10).Radius(k.R.MD).
						Border(1, p.Kyanite.Alpha(0.45)).Justify(ui.Start).AlignItems(ui.Center).FillWidth()
					if newBtn.Hovered() {
						newBtn.Background(p.KyaniteWash)
					}
					newBtn.Children(func() {
						ui.Row(c).Size(24, 24).Radius(k.R.SM).Background(p.Kyanite).Center().Children(func() {
							ui.Icon(c, theme.IconPlus).Size(13, 13).TextColor(theme.OnKyanite)
						})
						ui.Text(c, m.SidebarNewConnection()).FontWeight(500).TextColor(p.Ink).SingleLine()
					})
					if newBtn.Clicked() {
						s.openNewConnection()
					}

					qBtn := ui.ButtonBase(c.Key("welcome-new-query")).
						Height(40).PaddingX(12).Gap(10).Radius(k.R.MD).
						Border(1, p.Seam).Justify(ui.Start).AlignItems(ui.Center).FillWidth()
					if qBtn.Hovered() {
						qBtn.Background(p.Ink.Alpha(0.035))
					}
					qBtn.Children(func() {
						ui.Icon(c, theme.IconQuery).Size(14, 14).TextColor(p.InkFaint)
						ui.Text(c, m.ToolbarNewQuery()).FontWeight(500).TextColor(p.InkSoft).SingleLine()
					})
					if qBtn.Clicked() && s.actions.NewQuery != nil {
						s.actions.NewQuery()
					}
				})
			})

			// Quiet shortcut legend at bottom
			ui.Row(c).Padding(6, 0, 0, 0).Gap(16).AlignItems(ui.Center).Children(func() {
				hint := func(key, label string) {
					ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
						ui.Text(c, key).Font(theme.MonoFamily).FontSize(10.5).TextColor(p.InkFaint)
						ui.Text(c, label).FontSize(theme.SmallSize).TextColor(p.InkGhost)
					})
				}
				hint(cmdShortcut("K", "K"), m.PlateSwitchLabel())
				hint(cmdShortcut("N", "N"), m.SidebarNewConnection())
				hint(cmdShortcut("T", "T"), m.ToolbarNewQuery())
				hint(cmdShortcut("B", "B"), m.ToolbarToggleSidebar())
			})
		})
	})
}

// tabStrip sits on Datum #1 (34 DIP), flush with the sidebar connection header.
func (s *Shell) tabStrip(c *ui.Context, k theme.Set, tabs []Tab, activeTab *int) {
	p := k.P
	strip := ui.TabsBase(c, activeTab, len(tabs))
	strip.List.Height(theme.HeaderHeight).Background(p.Matrix).AlignItems(ui.Stretch).Children(func() {
		for i, tab := range tabs {
			active := i == *activeTab
			el := strip.Tab(i).Padding(0, 8, 0, 12).Gap(6).AlignItems(ui.Center).MinWidth(116).MaxWidth(220).
				BorderColor(p.Seam)
			hovered := el.Hovered()
			fg := p.InkFaint
			if active {
				el.Background(p.Canvas).BorderWidth(0, 1, 0, 0)
				fg = p.Ink
			} else {
				el.BorderWidth(0, 1, 1, 0)
				if hovered {
					fg = p.InkSoft
				}
			}
			el.Children(func() {
				if active {
					ui.Box(c.Key("active-tab-edge")).Absolute().Top(0).Left(0).Right(0).Height(2).
						Background(p.Kyanite).Transition(theme.GlideMatch("kyandb-active-tab-edge"))
				}
				icon := theme.IconTable
				if tab.Kind == TabQuery {
					icon = theme.IconQuery
				}
				ui.Icon(c, icon).Size(13, 13).TextColor(fg)
				ui.Text(c, tab.Title).TextColor(fg).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				if tab.Pending.Count() > 0 {
					ui.Box(c).Size(6, 6).Radius(3).Background(p.Amber).Label(msgs(c).TabUnsaved())
				}
				ui.Spacer(c)
				if active || hovered {
					closeBtn := ui.ButtonBase(c.Key(fmt.Sprintf("close-tab-%d", i))).
						Size(18, 18).Radius(k.R.XS).
						Label(msgs(c).TabClose()).
						Tooltip(withShortcut(msgs(c).TabClose(), cmdShortcut("W", "W")))
					cc := p.InkGhost
					if closeBtn.Hovered() {
						closeBtn.Background(p.Ink.Alpha(0.08))
						cc = p.Ink
					}
					closeBtn.Children(func() {
						ui.Icon(c, theme.IconClose).Size(10, 10).TextColor(cc)
					})
					if closeBtn.Clicked() && s.actions.CloseTab != nil {
						s.actions.CloseTab(i)
					}
				}
			})
		}
		ui.Row(c).Grow(1).PaddingX(6).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(p.Seam).Children(func() {
			if iconButton(c, k, "add-tab", theme.IconPlus,
				msgs(c).TabNew(),
				withShortcut(msgs(c).TabNew(), cmdShortcut("T", "T"))) && s.actions.NewQuery != nil {
				s.actions.NewQuery()
			}
		})
	})
}

// whereBar sits on Datum #2 (30 DIP), borderless and flush with the sidebar
// filter bar on the left—zero boxed input chrome over the data grid.
func (s *Shell) whereBar(c *ui.Context, k theme.Set, tabIdx int, v *tabView) {
	p := k.P
	ui.Row(c).Height(theme.SubBarHeight).PaddingX(12).Gap(8).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(p.Seam).Children(func() {
		ui.Text(c, "WHERE").Font(theme.MonoFamily).FontSize(theme.SmallSize).FontWeight(600).TextColor(p.InkGhost)
		in := ui.TextInputBase(c.Key("where-input"), &v.where).
			Font(theme.MonoFamily).
			Placeholder(msgs(c).FilterPlaceholder()).
			Label(msgs(c).FilterRowsLabel()).
			Grow(1)
		if in.Submitted() && s.actions.ApplyWhere != nil {
			v.whereSynced = v.where
			s.actions.ApplyWhere(tabIdx, v.where)
		}
		if v.where != "" {
			clearBtn := ui.ButtonBase(c.Key("clear-where")).
				Size(18, 18).Radius(k.R.XS).
				Label(msgs(c).SidebarClearFilter()).Tooltip(msgs(c).SidebarClearFilter())
			cc := p.InkFaint
			if clearBtn.Hovered() {
				clearBtn.Background(p.Ink.Alpha(0.08))
				cc = p.Ink
			}
			clearBtn.Children(func() {
				ui.Icon(c, theme.IconClose).Size(10, 10).TextColor(cc)
			})
			if clearBtn.Clicked() {
				v.where = ""
				v.whereSynced = ""
				if s.actions.ApplyWhere != nil {
					s.actions.ApplyWhere(tabIdx, "")
				}
			}
		}
	})
}

// queryBar also sits on Datum #2 (30 DIP) so switching between Table and
// Query tabs never shifts the horizontal grid line.
func (s *Shell) queryBar(c *ui.Context, k theme.Set, tabIdx int, tab Tab) {
	p := k.P
	ui.Row(c).Height(theme.SubBarHeight).PaddingX(8).Gap(6).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(p.Seam).Children(func() {
		if tab.Running {
			if button(c, k, "cancel-query", msgs(c).QueryCancel(), buttonDanger, theme.IconStop) && s.actions.CancelQuery != nil {
				s.actions.CancelQuery(tabIdx)
			}
			ui.Spinner(c).Size(12, 12).TextColor(p.Kyanite).Label(msgs(c).QueryRunning())
			ui.Text(c, msgs(c).QueryRunning()).FontSize(theme.SmallSize).TextColor(p.InkFaint)
		} else {
			if button(c, k, "run", msgs(c).QueryRun(), buttonPrimary, theme.IconPlay) && s.actions.RunQuery != nil {
				s.actions.RunQuery(tabIdx, s.currentQuerySQL(tabIdx, tab))
			}
			ui.Text(c, cmdShortcut("↵", "Enter")).Font(theme.MonoFamily).FontSize(theme.SmallSize).TextColor(p.InkGhost)
			ui.Box(c).Width(4)
			if button(c, k, "explain", msgs(c).QueryExplain(), buttonGhost, nil) && s.actions.ExplainQuery != nil {
				s.actions.ExplainQuery(tabIdx, s.currentQuerySQL(tabIdx, tab))
			}
		}
		ui.Spacer(c)
		if tab.Elapsed > 0 {
			ui.Text(c, formatElapsed(c, tab.Elapsed)).FontSize(theme.SmallSize).FontFeatures("tnum").TextColor(p.InkFaint)
		}
	})
}

func (s *Shell) errorBanner(c *ui.Context, k theme.Set, tabIdx int, tab Tab) {
	p := k.P
	ui.Row(c.Key("error-banner")).MinHeight(30).Padding(5, 12).Gap(8).AlignItems(ui.Center).
		Background(p.DeletedBg).BorderWidth(0, 0, 1, 0).BorderColor(p.Oxide.Alpha(0.28)).
		Transition(theme.BarRevealTransition).Children(func() {
		ui.Icon(c, theme.IconAlert).Size(13, 13).TextColor(p.Oxide)
		ui.Text(c, tab.Error).Font(theme.MonoFamily).FontSize(theme.SmallSize).TextColor(p.Ink).Grow(1).Shrink(1).MinWidth(0)
		if button(c, k, "error-retry", msgs(c).ErrorRetry(), buttonGhost, nil) {
			if tab.Kind == TabQuery && s.actions.RunQuery != nil {
				s.actions.RunQuery(tabIdx, s.currentQuerySQL(tabIdx, tab))
			} else if s.actions.Refresh != nil {
				s.actions.Refresh()
			}
		}
		dismiss := ui.ButtonBase(c.Key("error-dismiss")).
			Size(20, 20).Radius(k.R.XS).Label(msgs(c).ErrorDismiss()).Tooltip(msgs(c).ErrorDismiss())
		cc := p.InkSoft
		if dismiss.Hovered() {
			dismiss.Background(p.Ink.Alpha(0.08))
			cc = p.Ink
		}
		dismiss.Children(func() {
			ui.Icon(c, theme.IconClose).Size(11, 11).TextColor(cc)
		})
		if dismiss.Clicked() && s.actions.DismissError != nil {
			s.actions.DismissError(tabIdx)
		}
	})
}

var editorThemes = map[bool]*editor.Theme{}

func queryEditor(c *ui.Context, k theme.Set, v *tabView, tab Tab) {
	et := editorThemes[k.P.Dark]
	if et == nil {
		et = editor.DefaultTheme(k.UI)
		et.Background = k.P.Canvas
		et.ActiveLine = k.P.Ink.Alpha(0.025)
		et.LineNumber = k.P.InkGhost
		et.ActiveLineNumber = k.P.InkSoft
		editorThemes[k.P.Dark] = et
	}
	if v.editor == nil {
		buf := editor.NewBuffer(tab.Query)
		buf.SetLanguage(editor.LanguageFor("query.sql"))
		v.editor = &editor.State{Buffer: buf, Font: ui.Font{Family: theme.MonoFamily, Size: 12.5}, LineHeight: 1.6}
	}
	v.editor.Theme = et
	editor.View(c, v.editor).Fill()
}

const gutterWidth = 48

func (s *Shell) gridOrEmpty(c *ui.Context, k theme.Set, tabIdx int, v *tabView, tab Tab, conn *Connection) {
	if len(tab.Rows) == 0 && len(tab.Columns) == 0 && !tab.Running {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, msgs(c).GridNoRows()).TextColor(k.P.InkFaint)
		})
		return
	}
	s.grid(c, k, tabIdx, v, tab, conn)
}

func (s *Shell) grid(c *ui.Context, k theme.Set, tabIdx int, v *tabView, tab Tab, conn *Connection) {
	p := k.P
	m := msgs(c)
	editable := tab.Kind == TabTable && (conn == nil || !conn.ReadOnly) && s.actions.StageCellEdit != nil
	startEdit := func(row, colIdx int) {
		if row < 0 || row >= len(tab.Rows) || colIdx < 0 || colIdx >= len(tab.Rows[row].Cells) {
			return
		}
		cell := tab.Rows[row].Cells[colIdx]
		v.editActive = true
		v.editFresh = true
		v.editRow = row
		v.editCol = colIdx
		v.editOrig = cell.Text
		v.editOrigN = cell.Null
		if cell.Null {
			v.editDraft = ""
		} else {
			v.editDraft = cell.Text
		}
	}

	v.grid.CellsStyle = ui.CellsStyle{
		Cell:     p.KyaniteCell,
		CellText: p.KyaniteInk,
		Row:      p.KyaniteWash,
		Stripe:   p.Ink.Alpha(0.018),
	}
	v.grid.HeaderCell = func(cell ui.Element, col int) {
		cell.Background(p.Matrix).FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkSoft)
		if col > 0 && col <= len(tab.Columns) {
			column := tab.Columns[col-1]
			if column.Key {
				ui.Icon(c, theme.IconKey).Size(10, 10).TextColor(p.InkFaint).MarginStart(4)
			}
			if column.Type != "" {
				cell.Tooltip(column.Type)
			}
		}
	}
	v.grid.CellMenu = func(menu *ui.Menu, row, col int) {
		if row < 0 || row >= len(tab.Rows) || col <= 0 || col > len(tab.Columns) {
			return
		}
		colIdx := col - 1
		cell := tab.Rows[row].Cells[colIdx]
		if menu.Item(m.CellCopyValue()).Chosen() {
			if cell.Null {
				c.WriteClipboard("NULL")
			} else {
				c.WriteClipboard(cell.Text)
			}
		}
		if editable {
			menu.Separator()
			if menu.Item(m.CellEdit()).Chosen() {
				startEdit(row, colIdx)
			}
			if menu.Item(m.CellSetNull()).Chosen() {
				s.actions.StageCellEdit(tabIdx, row, colIdx, "", true)
			}
			if s.actions.StageDeleteRow != nil {
				menu.Separator()
				if menu.Item(m.CellDeleteRow()).Chosen() {
					s.actions.StageDeleteRow(tabIdx, row)
				}
			}
		}
	}
	cols := make([]ui.TableColumn, 0, len(tab.Columns)+2)
	cols = append(cols, ui.TableColumn{ID: "#", Width: gutterWidth, Fixed: true, RowHeader: true, Align: ui.End})
	for _, col := range tab.Columns {
		tc := ui.TableColumn{Title: col.Name, Width: columnWidth(col), MinWidth: 48, Sortable: true}
		if col.Numeric {
			tc.Align = ui.End
		}
		cols = append(cols, tc)
	}
	cols = append(cols, ui.TableColumn{ID: "_tail"})
	ui.Themed(c, k.Grid).Grow(1).Children(func() {
		ui.Table(c, &v.grid, cols, len(tab.Rows), func(row, col int) {
			r := tab.Rows[row]
			if col == 0 {
				gutterCell(c, p, row, r.Change)
				return
			}
			if col > len(tab.Columns) {
				return
			}
			colIdx := col - 1
			if v.editActive && v.editRow == row && v.editCol == colIdx {
				s.inlineCellEditor(c, k, tabIdx, v, row, colIdx)
				return
			}
			if dataCell(c, k, r, tab.Columns[colIdx], r.Cells[colIdx], editable) {
				startEdit(row, colIdx)
			}
		}).Fill().Label(tab.Title)
	})
}

func (s *Shell) inlineCellEditor(c *ui.Context, k theme.Set, tabIdx int, v *tabView, row, colIdx int) {
	p := k.P
	in := ui.TextInputBase(c.Key(fmt.Sprintf("cell-edit-%d-%d", row, colIdx)), &v.editDraft).
		Grow(1).MinWidth(0).Height(22).PaddingX(4).Radius(k.R.XS).
		Background(p.Canvas).Border(1, p.Kyanite).TextColor(p.Ink).
		Label(msgs(c).CellEdit())
	if v.editFresh {
		v.editFresh = false
		in.Focus()
	}
	if in.Submitted() {
		v.editActive = false
		if s.actions.StageCellEdit != nil && (v.editOrigN || v.editDraft != v.editOrig) {
			s.actions.StageCellEdit(tabIdx, row, colIdx, v.editDraft, false)
		}
		return
	}
	if in.Shortcut(0, ui.KeyEscape) {
		v.editActive = false
	}
}

// gutterCell carries the row number and a quiet color signal for staged rows.
func gutterCell(c *ui.Context, p *theme.Palette, row int, ch Change) {
	text := formatInt(c, int64(row+1))
	t := ui.Text(c, text).Font(theme.MonoFamily).FontSize(10.5).SingleLine()
	switch ch {
	case Modified:
		t.TextColor(p.AmberInk).FontWeight(600)
	case Inserted:
		t.TextColor(p.Jade).FontWeight(600)
	case Deleted:
		t.TextColor(p.Oxide).FontWeight(600)
	default:
		t.TextColor(p.InkGhost)
	}
}

// dataCell renders values with calm surface washes for staged changes—never
// neon text, pill boxes, or dotted underlines. Returns true when double-clicked
// on an editable table cell.
func dataCell(c *ui.Context, k theme.Set, r Row, col Column, cell Cell, editable bool) bool {
	p := k.P
	var wash ui.Color
	switch {
	case r.Change == Deleted:
		wash = p.DeletedBg
	case r.Change == Inserted:
		wash = p.InsertedBg
	case r.Change == Modified && cell.Staged:
		wash = p.ModifiedBg
	}

	renderVal := func() {
		if cell.Null {
			t := ui.Text(c, "NULL").FontSize(11).Italic().TextColor(p.InkGhost).SingleLine()
			if r.Change == Deleted {
				t.Strikethrough()
			}
			return
		}
		t := ui.Text(c, cell.Text).SingleLine().Ellipsis("…")
		if col.Numeric {
			t.FontFeatures("tnum")
		}
		if r.Change == Deleted {
			t.TextColor(p.InkFaint).Strikethrough()
		}
	}

	wrap := ui.Row(c).Grow(1).FillHeight().AlignItems(ui.Center)
	if wash != ui.Transparent {
		wrap.PaddingX(4).Radius(k.R.XS).Background(wash)
	}
	if col.Numeric {
		wrap.Justify(ui.End)
	}
	wrap.Children(renderVal)
	return editable && wrap.DoubleClicked()
}

// ledger summarizes staged edits at the bottom of a modified table tab.
func (s *Shell) ledger(c *ui.Context, k theme.Set, tabIdx int, tab Tab, conn *Connection) {
	pend := tab.Pending
	p := k.P
	ui.Column(c.Key("ledger-bar")).FillWidth().Transition(theme.BarRevealTransition).Children(func() {
		seam(c, p.Seam)
		ui.Row(c).Height(34).PaddingX(12).Gap(10).AlignItems(ui.Center).Background(p.Matrix).Children(func() {
			ui.Text(c, msgs(c).LedgerPending(pend.Count())).FontWeight(500).TextColor(p.Ink)
			tally := func(sign string, n int, color ui.Color) {
				if n > 0 {
					ui.Text(c, fmt.Sprintf("%s%d", sign, n)).Font(theme.MonoFamily).FontSize(theme.SmallSize).FontWeight(500).TextColor(color)
				}
			}
			tally("~", pend.Modified, p.AmberInk)
			tally("+", pend.Inserted, p.Jade)
			tally("−", pend.Deleted, p.Oxide)
			ui.Spacer(c)
			if button(c, k, "ledger-discard", msgs(c).LedgerDiscard(), buttonGhost, nil) && s.actions.DiscardStaged != nil {
				s.actions.DiscardStaged(tabIdx)
			}
			if button(c, k, "ledger-review", msgs(c).LedgerReview(), buttonGhost, nil) {
				s.reviewTabIdx = tabIdx
				pay := modal.ReviewSQLPayload{
					Modified:   pend.Modified,
					Inserted:   pend.Inserted,
					Deleted:    pend.Deleted,
					Statements: pend.Count(),
					SQL:        tab.StagedSQL,
				}
				if conn != nil {
					pay.ConnectionName = conn.Name
					pay.Env = conn.Env
					pay.ReadOnly = conn.ReadOnly
				}
				s.reviewModal.OpenWith(pay)
				if s.actions.ReviewSQL != nil {
					s.actions.ReviewSQL(tabIdx)
				}
			}
			switch {
			case conn != nil && conn.ReadOnly:
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, theme.IconLock).Size(11, 11).TextColor(p.InkFaint)
					ui.Text(c, msgs(c).LedgerReadOnly()).FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkFaint).SingleLine()
				})
			case conn != nil && conn.Env.Protected():
				if button(c, k, "ledger-commit", msgs(c).LedgerCommit(string(conn.Env)), buttonDanger, nil) && s.actions.CommitStaged != nil {
					s.actions.CommitStaged(tabIdx)
				}
			default:
				if button(c, k, "ledger-commit", msgs(c).LedgerCommit(""), buttonPrimary, nil) && s.actions.CommitStaged != nil {
					s.actions.CommitStaged(tabIdx)
				}
			}
		})
	})
}

func columnWidth(col Column) float32 {
	switch {
	case col.Key:
		return 76
	case col.Numeric:
		return 96
	default:
		return 172
	}
}
