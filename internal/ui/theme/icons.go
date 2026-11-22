package theme

import (
	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/assets"
)

// LogoMark is KyanDB's embedded brand emblem parsed once from assets/logo.svg.
var LogoMark = ui.MustParseSVG(assets.LogoSVG)

// Icons are drawn in-house on a 24 DIP grid with one 1.75 stroke so they
// share a single weight; ui.Icon tints them with the current text color.
func icon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	IconTable    = icon(`<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M3.5 9.5h17M9.5 9.5v10"/>`)
	IconView     = icon(`<path d="M2.5 12s3.5-6.5 9.5-6.5 9.5 6.5 9.5 6.5-3.5 6.5-9.5 6.5S2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.75"/>`)
	IconFunction = icon(`<path d="M15.5 4.5h-1.75a3 3 0 0 0-3 3v12"/><path d="M7.5 11h7"/>`)
	IconQuery    = icon(`<path d="M8.5 8.5 5 12l3.5 3.5M15.5 8.5 19 12l-3.5 3.5"/>`)
	IconLock     = icon(`<rect x="5" y="10.5" width="14" height="9.5" rx="2"/><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"/>`)
	IconPlay     = icon(`<path d="M8 5.5v13l10.5-6.5z"/>`)
	IconPlus     = icon(`<path d="M12 5.5v13M5.5 12h13"/>`)
	IconSidebar  = icon(`<rect x="3.5" y="4.5" width="17" height="15" rx="2"/><path d="M9.5 4.5v15"/>`)
	IconRefresh  = icon(`<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3"/><path d="M19.5 4.5v4.5H15"/>`)
	IconChevron  = icon(`<path d="m9.5 6.5 5.5 5.5-5.5 5.5"/>`)
	IconChevDown = icon(`<path d="m6.5 9.5 5.5 5.5 5.5-5.5"/>`)
	IconKey      = icon(`<circle cx="8" cy="15.5" r="3.5"/><path d="m10.5 13 8-8M16 7.5l2.5 2.5"/>`)
	IconClose    = icon(`<path d="m7 7 10 10M17 7 7 17"/>`)
	IconFilter   = icon(`<path d="M4.5 6h15M7.5 12h9M10.5 18h3"/>`)
	IconCheck    = icon(`<path d="m5 12.5 4.5 4.5L19 7.5"/>`)
	IconAlert    = icon(`<circle cx="12" cy="12" r="8.5"/><path d="M12 8v4.5M12 16h.01"/>`)
	IconStop     = icon(`<rect x="6.5" y="6.5" width="11" height="11" rx="1.5"/>`)
	IconSearch   = icon(`<circle cx="10.5" cy="10.5" r="6"/><path d="m15 15 4.5 4.5"/>`)
)
