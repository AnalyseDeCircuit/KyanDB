// Package theme defines KyanDB's native desktop appearance: calm neutral
// planes, quiet hairlines, restrained mineral kyanite accents, and soft
// surface washes for staged database edits.
package theme

import (
	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

// Dense desktop metrics on a 4 DIP grid.
// TitleBarHeight (28) sits above two aligned horizontal datums:
// HeaderHeight (34: sidebar connection bar + workspace tab strip) and
// SubBarHeight (30: borderless sidebar filter + borderless WHERE / query bar).
const (
	FontSize       = 12
	SmallSize      = 11
	RowHeight      = 24
	TitleBarHeight = 28
	HeaderHeight   = 34
	SubBarHeight   = 30
	DefaultRadius  = 6
	MaxRadius      = 16
	SidebarW       = 236
	MonoFamily     = "monospace"
)

// OnKyanite is text on primary mineral-kyanite buttons.
var OnKyanite = ui.Hex("#ffffff")

// OnDanger is text on solid destructive buttons.
var OnDanger = ui.Hex("#ffffff")

// Radii derives the corner scale from one user-adjustable base radius
// (0..16 DIP, default 6). mygo's GPU shaders and software rasterizer draw
// every Element.Radius with Core Animation's continuous superellipse curve
// (QuartzCore supercircle SDF) automatically.
type Radii struct {
	XS float32 // micro indicators, tab close hits
	SM float32 // compact buttons, rows, inputs
	MD float32 // welcome panels, standard controls
	LG float32 // floating overlays: modals, popovers, dialogs
}

// RadiiFor computes the proportional corner scale from a single base radius.
func RadiiFor(base float32) Radii {
	r := min(max(base, 0), MaxRadius)
	if r == 0 {
		return Radii{}
	}
	return Radii{
		XS: max(r-4, 0),
		SM: max(r-2, 0),
		MD: r,
		LG: r + 4,
	}
}

// Palette holds the named tones every view draws from. Every text-carrying
// tone meets WCAG AA (>= 4.5:1) against its intended surface.
type Palette struct {
	Dark bool

	Canvas ui.Color // data surfaces: grid, editor, active tab
	Matrix ui.Color // chrome: titlebar, sidebar, tab strip, bars
	Inset  ui.Color // recessed wells inside modals and popovers

	Seam       ui.Color // low-alpha 1 DIP hairlines between regions
	SeamStrong ui.Color // subtle control outlines

	Ink      ui.Color // primary text and values
	InkSoft  ui.Color // labels, header text, secondary buttons
	InkFaint ui.Color // metadata, routes, inactive tabs, icons
	InkGhost ui.Color // NULL, row numbers, counts, chevrons

	Kyanite     ui.Color // primary button fill, active tab bar, focus ring
	KyaniteInk  ui.Color // accent text / cursor outline on Canvas or KyaniteCell
	KyaniteWash ui.Color // selected grid rows (quiet neutral-teal tint)
	KyaniteCell ui.Color // cursor cell inside the selection

	Oxide     ui.Color // prod signal, delete gutter, error icon/border
	OxideFill ui.Color // muted crimson destructive CTA fill (white >= 5.7:1)
	Amber     ui.Color // staging dot/indicator
	AmberInk  ui.Color // warm ochre text for modified gutter/tally
	Jade      ui.Color // dev signal, inserted gutter/tally

	// Soft surface washes for staged table edits: calm cell tinting replaces
	// garish colored text or dotted underlines.
	ModifiedBg ui.Color
	InsertedBg ui.Color
	DeletedBg  ui.Color
}

var (
	lightPalette = &Palette{
		Canvas:      ui.Hex("#ffffff"),
		Matrix:      ui.Hex("#f6f8fa"),
		Inset:       ui.Hex("#eef1f4"),
		Seam:        ui.RGBA(15, 23, 32, 0.08),
		SeamStrong:  ui.RGBA(15, 23, 32, 0.14),
		Ink:         ui.Hex("#1b2025"),
		InkSoft:     ui.Hex("#434c56"),
		InkFaint:    ui.Hex("#5c6670"),
		InkGhost:    ui.Hex("#65707b"),
		Kyanite:     ui.Hex("#0a7a88"),
		KyaniteInk:  ui.Hex("#07606b"),
		KyaniteWash: ui.RGBA(10, 122, 136, 0.07),
		KyaniteCell: ui.RGBA(10, 122, 136, 0.16),
		Oxide:       ui.Hex("#bd3238"),
		OxideFill:   ui.Hex("#bd3238"),
		Amber:       ui.Hex("#b88014"),
		AmberInk:    ui.Hex("#8a5e08"),
		Jade:        ui.Hex("#1b784b"),
		ModifiedBg:  ui.RGBA(212, 148, 28, 0.13),
		InsertedBg:  ui.RGBA(27, 136, 82, 0.10),
		DeletedBg:   ui.RGBA(189, 50, 56, 0.09),
	}
	darkPalette = &Palette{
		Dark:        true,
		Canvas:      ui.Hex("#181a1d"),
		Matrix:      ui.Hex("#1d2024"),
		Inset:       ui.Hex("#131518"),
		Seam:        ui.RGBA(255, 255, 255, 0.07),
		SeamStrong:  ui.RGBA(255, 255, 255, 0.13),
		Ink:         ui.Hex("#e4e7eb"),
		InkSoft:     ui.Hex("#aeb6bf"),
		InkFaint:    ui.Hex("#8c95a0"),
		InkGhost:    ui.Hex("#828c97"),
		Kyanite:     ui.Hex("#0b7c8a"),
		KyaniteInk:  ui.Hex("#58c4d2"),
		KyaniteWash: ui.RGBA(14, 138, 153, 0.11),
		KyaniteCell: ui.RGBA(14, 138, 153, 0.24),
		Oxide:       ui.Hex("#e56b70"),
		OxideFill:   ui.Hex("#b8373d"),
		Amber:       ui.Hex("#d4a03c"),
		AmberInk:    ui.Hex("#d9a94e"),
		Jade:        ui.Hex("#4eb882"),
		ModifiedBg:  ui.RGBA(212, 154, 42, 0.14),
		InsertedBg:  ui.RGBA(64, 174, 118, 0.12),
		DeletedBg:   ui.RGBA(212, 76, 82, 0.12),
	}

	defaultLight = makeSet(lightPalette, DefaultRadius)
	defaultDark  = makeSet(darkPalette, DefaultRadius)
)

// Set is everything one appearance needs; values are shared and must not be
// mutated by callers.
type Set struct {
	P       *Palette
	R       Radii
	UI      *ui.Theme
	Sidebar *ui.Theme
	Grid    *ui.Theme
}

// For returns the cached theme set matching the system appearance at the
// default base radius.
func For(dark bool) Set {
	if dark {
		return defaultDark
	}
	return defaultLight
}

// ForWith returns a theme set scaled to a user-chosen base corner radius
// (0..16 DIP). The default radius hits the cached set with zero allocation.
func ForWith(dark bool, baseRadius float32) Set {
	clamped := min(max(baseRadius, 0), MaxRadius)
	if clamped == DefaultRadius {
		return For(dark)
	}
	if dark {
		return makeSet(darkPalette, clamped)
	}
	return makeSet(lightPalette, clamped)
}

func makeSet(p *Palette, baseRadius float32) Set {
	r := RadiiFor(baseRadius)
	base := build(p, r)
	return Set{
		P:       p,
		R:       r,
		UI:      base,
		Sidebar: sidebar(p, base),
		Grid:    grid(p, base),
	}
}

func build(p *Palette, r Radii) *ui.Theme {
	var t *ui.Theme
	if p.Dark {
		t = ui.DarkTheme()
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#24282d"), ui.Hex("#2c3137"), ui.Hex("#343a42")
		t.Scrollbar = ui.RGBA(255, 255, 255, 0.18)
	} else {
		t = ui.LightTheme()
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#f0f3f6"), ui.Hex("#e6eaee"), ui.Hex("#dce1e6")
		t.Scrollbar = ui.RGBA(0, 0, 0, 0.18)
	}
	t.Background = p.Canvas
	t.Border = p.SeamStrong
	t.Text, t.TextMuted = p.Ink, p.InkFaint
	t.Accent = p.Kyanite
	t.AccentHover = p.Kyanite.Mix(ui.Hex("#ffffff"), 0.10)
	t.AccentPressed = p.Kyanite.Mix(ui.Hex("#000000"), 0.10)
	t.AccentText = OnKyanite
	t.InactiveAccent = p.Kyanite
	t.Danger, t.Warning, t.Success = p.Oxide, p.Amber, p.Jade
	t.Selection = p.Kyanite.Alpha(0.24)
	t.Focus = p.Kyanite.Alpha(0.45)
	t.FocusRingWidth, t.FocusRingOffset = 1.5, 0
	t.ScrollbarWidth = 5
	t.Radius = r.MD
	t.Spacing = 4
	t.FontSize = FontSize
	return t
}

// sidebar keeps the source list quiet: the chosen row is a soft wash with
// ink text instead of a solid accent bar.
func sidebar(p *Palette, base *ui.Theme) *ui.Theme {
	t := *base
	t.Background = p.Matrix
	t.Accent = p.Kyanite.Alpha(0.13)
	t.InactiveAccent = p.Ink.Alpha(0.05)
	t.AccentText = p.Ink
	t.SurfaceHover = p.Ink.Alpha(0.035)
	return &t
}

// grid tightens the table's spacing unit: ui.Table derives its 8-unit
// header height and 2.5-unit cell padding from Theme.Spacing, so 3.25 gives a
// 26 DIP header and 8 DIP cell insets.
func grid(p *Palette, base *ui.Theme) *ui.Theme {
	t := *base
	t.Surface = p.Matrix
	t.Spacing = 3.25
	return &t
}

// Env maps an environment to its signal color.
func (p *Palette) Env(env domain.Environment) ui.Color {
	switch env {
	case domain.EnvProd:
		return p.Oxide
	case domain.EnvStaging:
		return p.Amber
	default:
		return p.Jade
	}
}
