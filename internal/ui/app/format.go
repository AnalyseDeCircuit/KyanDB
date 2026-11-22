package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/locales"
)

// msgs returns the catalog in the window's language. Views read the
// window's locale rather than the process-wide one so a frame, and a
// Tester, always agree on the language.
func msgs(c *ui.Context) locales.Messages { return locales.In(c.Locale()) }

func engineLabel(conn *Connection) string {
	if conn.Version == "" {
		return string(conn.Engine)
	}
	return string(conn.Engine) + " " + conn.Version
}

// kindName is the select key a catalog message branches on.
func kindName(k ObjectKind) string {
	switch k {
	case ObjectView:
		return "view"
	case ObjectFunction:
		return "function"
	default:
		return "table"
	}
}

func formatInt(c *ui.Context, n int64) string {
	return i18n.FormatNumber(c.Locale(), n, i18n.NumberOptions{})
}

// formatCompact renders a row estimate in the locale's short form
// (128K, 4.2M; 13万 in Chinese).
func formatCompact(c *ui.Context, n int64) string {
	return i18n.FormatNumber(c.Locale(), n, i18n.NumberOptions{Notation: i18n.CompactShort})
}

func formatElapsed(c *ui.Context, d time.Duration) string {
	if d < time.Second {
		return msgs(c).ElapsedMs(int(d.Milliseconds()))
	}
	return msgs(c).ElapsedSeconds(d.Seconds())
}

func rowsLabel(c *ui.Context, shown int, total int64) string {
	if total > int64(shown) {
		return msgs(c).StatusRowsOf(shown, int(total))
	}
	return msgs(c).StatusRows(shown)
}

// cmdShortcut formats a primary-modifier shortcut for the active platform
// (⌘B / ⌘↵ on macOS; Ctrl+B / Ctrl+Enter elsewhere).
func cmdShortcut(macKey, otherKey string) string {
	if ui.Cmd == ui.Super {
		return "⌘" + macKey
	}
	return "Ctrl+" + otherKey
}

func withShortcut(label, shortcut string) string {
	return fmt.Sprintf("%s (%s)", label, shortcut)
}
