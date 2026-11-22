package app

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

// objectKey identifies a sidebar row: a kind section, or an object by index
// into the active connection's Objects.
type objectKey struct {
	Section bool
	Kind    ObjectKind
	Index   int
}

const sidebarRow = 24

var sectionOrder = [...]ObjectKind{ObjectTable, ObjectView, ObjectFunction}

// objectIndex groups and filters the active connection's objects. It is
// rebuilt only when its inputs change so a frame never rescans the schema.
type objectIndex struct {
	state   ui.OutlineState[objectKey]
	chosen  int
	filter  string
	builtAt indexInputs
	roots   []objectKey
	byKind  map[ObjectKind][]objectKey
}

type indexInputs struct {
	generation uint64
	active     int
	filter     string
}

func (x *objectIndex) init() {
	x.chosen = -1
	x.state.List.Selected = &x.chosen
	// Section headers (TABLES/VIEWS/FUNCTIONS) toggle open/closed rather than
	// entering object selection.
	x.state.List.Header = func(row int) bool {
		return x.state.Item(row).Section
	}
	x.state.Indent = 0
	for _, k := range sectionOrder {
		x.state.Open.Add(objectKey{Section: true, Kind: k})
	}
}

func (x *objectIndex) refresh(snap Snapshot, conn *Connection) {
	in := indexInputs{generation: snap.Generation, active: snap.Active, filter: x.filter}
	if x.byKind != nil && in == x.builtAt {
		return
	}
	x.builtAt = in
	x.byKind = map[ObjectKind][]objectKey{}
	x.roots = x.roots[:0]
	needle := strings.ToLower(x.filter)
	for i, o := range conn.Objects {
		if needle != "" && !strings.Contains(strings.ToLower(o.Name), needle) {
			continue
		}
		x.byKind[o.Kind] = append(x.byKind[o.Kind], objectKey{Kind: o.Kind, Index: i})
	}
	for _, k := range sectionOrder {
		if len(x.byKind[k]) > 0 {
			x.roots = append(x.roots, objectKey{Section: true, Kind: k})
		}
	}
	x.state.Generation++
}

func (s *Shell) sidebar(c *ui.Context, k theme.Set, snap Snapshot, conn *Connection) {
	p := k.P
	ui.Themed(c, k.Sidebar).Fill().Children(func() {
		// Datum #1 (34 DIP): connection switcher header aligned flush with
		// the workspace tab strip.
		var anchor ui.Element
		ui.Row(c).Height(theme.HeaderHeight).PaddingX(8).Gap(4).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(p.Seam).Children(func() {
			anchor = ui.ButtonBase(c.Key("sidebar-conn-switch")).
				Height(24).PaddingX(6).Gap(6).Radius(k.R.SM).
				Grow(1).MinWidth(0).Justify(ui.Start).AlignItems(ui.Center).
				Label(msgs(c).PlateSwitchLabel()).
				Tooltip(withShortcut(msgs(c).PlateSwitchLabel(), cmdShortcut("K", "K")))
			if anchor.Hovered() || s.connMenuOpen {
				anchor.Background(p.Ink.Alpha(0.05))
			}
			anchor.Children(func() {
				if conn == nil {
					ui.Text(c, msgs(c).PlateNoConnection()).FontWeight(500).TextColor(p.InkFaint).SingleLine()
				} else {
					ui.Box(c).Size(6, 6).Radius(3).Shrink(0).Background(p.Env(conn.Env))
					ui.Text(c, conn.Name).FontWeight(600).TextColor(p.Ink).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
				}
				ui.Icon(c, theme.IconChevDown).Size(11, 11).Shrink(0).TextColor(p.InkGhost)
			})
			if anchor.Clicked() {
				s.connMenuOpen = !s.connMenuOpen
			}
			if iconButton(c, k, "sidebar-new-conn", theme.IconPlus,
				msgs(c).SidebarNewConnection(),
				withShortcut(msgs(c).SidebarNewConnection(), cmdShortcut("N", "N"))) {
				s.openNewConnection()
			}
		})
		s.connectionPopover(c, k, anchor, snap)

		if conn == nil {
			ui.Column(c).Grow(1).Center().Gap(8).Padding(16).Children(func() {
				ui.Text(c, msgs(c).SidebarNoConnections()).TextColor(p.InkFaint)
				if button(c, k, "new-connection", msgs(c).SidebarNewConnection(), buttonOutline, theme.IconPlus) {
					s.openNewConnection()
				}
			})
			return
		}

		// Datum #2 (30 DIP): borderless inline filter bar aligned flush with
		// the workspace WHERE / query action bar.
		x := &s.objects
		ui.Row(c).Height(theme.SubBarHeight).PaddingX(10).Gap(6).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(p.Seam).Children(func() {
			ui.Icon(c, theme.IconSearch).Size(12, 12).TextColor(p.InkGhost)
			ui.TextInputBase(c.Key("sidebar-filter"), &x.filter).
				Placeholder(msgs(c).SidebarFilter()).
				Label(msgs(c).SidebarFilterLabel()).
				Grow(1)
			if x.filter != "" {
				clearBtn := ui.ButtonBase(c.Key("sidebar-filter-clear")).
					Size(16, 16).Radius(k.R.XS).Label(msgs(c).SidebarClearFilter())
				if clearBtn.Hovered() {
					clearBtn.Background(p.Ink.Alpha(0.08))
				}
				clearBtn.Children(func() {
					ui.Icon(c, theme.IconClose).Size(10, 10).TextColor(p.InkFaint)
				})
				if clearBtn.Clicked() {
					x.filter = ""
				}
			}
		})

		x.refresh(snap, conn)
		if x.filter != "" && len(x.roots) == 0 {
			ui.Column(c).Grow(1).Center().Gap(8).Padding(16).Children(func() {
				ui.Text(c, msgs(c).SidebarNoMatches()).TextColor(p.InkFaint)
				if button(c, k, "clear-sidebar-filter", msgs(c).SidebarClearFilter(), buttonGhost, nil) {
					x.filter = ""
				}
			})
			return
		}
		x.state.Prefix = func(r ui.OutlineRow) {
			ui.Box(c).Width(4 + float32(r.Depth)*14)
			arrow := r.Arrow().Size(14, 14).Center()
			if r.Branch {
				icon := theme.IconChevron
				if r.Open {
					icon = theme.IconChevDown
				}
				arrow.Children(func() { ui.Icon(c, icon).Size(10, 10).TextColor(p.InkGhost) })
			}
		}
		outline := ui.Outline(c, &x.state, x.roots, func(key objectKey) []objectKey {
			if key.Section {
				return x.byKind[key.Kind]
			}
			return nil
		}, func(key objectKey) {
			if key.Section {
				if sectionRow(c, p, key, len(x.byKind[key.Kind])) {
					if x.state.Open.Has(key) {
						x.state.Open.Remove(key)
					} else {
						x.state.Open.Add(key)
					}
				}
				return
			}
			obj := conn.Objects[key.Index]
			if objectRow(c, p, obj) && s.actions.OpenTable != nil && obj.Kind != ObjectFunction {
				s.actions.OpenTable(obj.Name)
			}
		}).Grow(1).Padding(4, 6, 4, 6).Label(msgs(c).SidebarObjectsLabel())
		if outline.Submitted() && x.chosen >= 0 && x.chosen < x.state.Rows() && s.actions.OpenTable != nil {
			key := x.state.Item(x.chosen)
			if !key.Section && key.Index >= 0 && key.Index < len(conn.Objects) {
				obj := conn.Objects[key.Index]
				if obj.Kind != ObjectFunction {
					s.actions.OpenTable(obj.Name)
				}
			}
		}
	})
}

func sectionRow(c *ui.Context, p *theme.Palette, key objectKey, n int) bool {
	b := ui.ButtonBase(c.Key(fmt.Sprintf("section-%d", key.Kind))).
		Grow(1).Height(sidebarRow).Gap(6).PaddingEnd(8).AlignItems(ui.Center)
	b.Children(func() {
		ui.Text(c, msgs(c).SidebarSection(kindName(key.Kind))).FontSize(10.5).FontWeight(600).LetterSpacing(0.4).TextColor(p.InkFaint)
		ui.Spacer(c)
		ui.Text(c, formatInt(c, int64(n))).FontSize(theme.SmallSize).FontFeatures("tnum").TextColor(p.InkGhost)
	})
	return b.Clicked()
}

func objectRow(c *ui.Context, p *theme.Palette, o Object) bool {
	b := ui.ButtonBase(c.Key(fmt.Sprintf("obj-%d-%s", o.Kind, o.Name))).
		Grow(1).Height(sidebarRow).Gap(7).PaddingEnd(8).AlignItems(ui.Center).Justify(ui.Start)
	b.Children(func() {
		ui.Icon(c, objectIcon(o.Kind)).Size(13, 13).TextColor(p.InkFaint)
		ui.Text(c, o.Name).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
		ui.Spacer(c)
		if o.Rows >= 0 {
			ui.Text(c, formatCompact(c, o.Rows)).FontSize(theme.SmallSize).FontFeatures("tnum").TextColor(p.InkGhost)
		}
	})
	return b.Clicked()
}

func objectIcon(k ObjectKind) *ui.SVG {
	switch k {
	case ObjectView:
		return theme.IconView
	case ObjectFunction:
		return theme.IconFunction
	default:
		return theme.IconTable
	}
}
