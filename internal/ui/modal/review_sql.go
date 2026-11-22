package modal

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

// ReviewSQLPayload is the immutable staged transaction diff shown for
// review before execution.
type ReviewSQLPayload struct {
	ConnectionName string
	Env            domain.Environment
	ReadOnly       bool
	Modified       int
	Inserted       int
	Deleted        int
	Statements     int
	SQL            string
}

// ReviewSQLState owns the transient view state of the Review SQL modal.
type ReviewSQLState struct {
	Open    bool
	Payload ReviewSQLPayload
	Copied  bool
	editor  *editor.State
}

// OpenWith displays the modal with the given SQL diff payload.
func (s *ReviewSQLState) OpenWith(payload ReviewSQLPayload) {
	s.Open = true
	s.Payload = payload
	s.Copied = false
	buf := editor.NewBuffer(payload.SQL)
	buf.SetLanguage(editor.LanguageFor("diff.sql"))
	s.editor = &editor.State{
		Buffer:     buf,
		ReadOnly:   true,
		SoftWrap:   true,
		Font:       ui.Font{Family: theme.MonoFamily, Size: 12.5},
		LineHeight: 1.6,
	}
}

// Close hides the modal and drops its preview buffer.
func (s *ReviewSQLState) Close() {
	s.Open = false
	s.Copied = false
	s.editor = nil
}

// ReviewSQLCallbacks receives user decisions from the Review SQL modal.
type ReviewSQLCallbacks struct {
	OnCommit  func()
	OnDiscard func()
}

var reviewEditorThemes = map[bool]*editor.Theme{}

// ReviewSQL renders the SQL diff review modal when state.Open is true.
func ReviewSQL(c *ui.Context, k theme.Set, state *ReviewSQLState, cb ReviewSQLCallbacks) {
	if !state.Open {
		return
	}
	p := k.P
	m := msgs(c)
	pay := state.Payload

	frame(c, k, &state.Open, 640, func() {
		state.Close()
	}, func() {
		header(c, k, m.ReviewTitle(), pay.ConnectionName, pay.Env, pay.ConnectionName != "", state.Close)

		// Summary strip: tallies, statement count, Copy SQL
		ui.Row(c).Height(32).PaddingX(16).Gap(10).AlignItems(ui.Center).Background(p.Matrix).Children(func() {
			tally := func(sign string, n int, color ui.Color) {
				if n > 0 {
					ui.Text(c, fmt.Sprintf("%s%d", sign, n)).Font(theme.MonoFamily).FontSize(theme.SmallSize).FontWeight(500).TextColor(color)
				}
			}
			tally("~", pay.Modified, p.AmberInk)
			tally("+", pay.Inserted, p.Jade)
			tally("−", pay.Deleted, p.Oxide)
			stmts := pay.Statements
			if stmts <= 0 {
				stmts = pay.Modified + pay.Inserted + pay.Deleted
			}
			ui.Text(c, m.ReviewSummary(stmts)).FontSize(theme.SmallSize).TextColor(p.InkFaint)
			ui.Spacer(c)
			copyLabel := m.ReviewCopy()
			if state.Copied {
				copyLabel = m.ReviewCopied()
			}
			if actionButton(c, k, "review-copy", copyLabel, buttonGhost, nil) {
				c.WriteClipboard(pay.SQL)
				state.Copied = true
			}
		})
		seam(c, p.Seam)

		// Read-only SQL preview
		et := reviewEditorThemes[p.Dark]
		if et == nil {
			et = editor.DefaultTheme(k.UI)
			et.Background = p.Canvas
			et.ActiveLine = ui.Transparent
			et.LineNumber = p.InkGhost
			et.ActiveLineNumber = p.InkFaint
			reviewEditorThemes[p.Dark] = et
		}
		if state.editor == nil {
			state.OpenWith(pay)
		}
		state.editor.Theme = et
		ui.Column(c).Height(260).Background(p.Canvas).Children(func() {
			editor.View(c, state.editor).Fill()
		})

		// Protected-environment notice
		if pay.Env.Protected() && !pay.ReadOnly {
			ec := p.Env(pay.Env)
			seam(c, p.Seam)
			ui.Row(c).Padding(8, 16).Gap(8).AlignItems(ui.Center).Background(p.DeletedBg).Children(func() {
				ui.Icon(c, theme.IconAlert).Size(13, 13).TextColor(ec)
				ui.Text(c, m.ReviewProdNotice(pay.ConnectionName)).FontSize(theme.SmallSize).TextColor(p.InkSoft).Grow(1).Shrink(1).MinWidth(0)
			})
		}

		// Footer
		seam(c, p.Seam)
		ui.Row(c).Height(46).PaddingX(16).Gap(8).AlignItems(ui.Center).Background(p.Matrix).Children(func() {
			if actionButton(c, k, "review-discard", m.LedgerDiscard(), buttonGhost, nil) {
				state.Close()
				if cb.OnDiscard != nil {
					cb.OnDiscard()
				}
			}
			ui.Spacer(c)
			if actionButton(c, k, "review-cancel", m.ModalCancel(), buttonGhost, nil) {
				state.Close()
			}
			switch {
			case pay.ReadOnly:
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, theme.IconLock).Size(12, 12).TextColor(p.InkFaint)
					ui.Text(c, m.LedgerReadOnly()).FontSize(theme.SmallSize).FontWeight(500).TextColor(p.InkFaint).SingleLine()
				})
			case pay.Env.Protected():
				if actionButton(c, k, "review-commit", m.LedgerCommit(string(pay.Env)), buttonDanger, nil) {
					state.Close()
					if cb.OnCommit != nil {
						cb.OnCommit()
					}
				}
			default:
				if actionButton(c, k, "review-commit", m.LedgerCommit(""), buttonPrimary, nil) {
					state.Close()
					if cb.OnCommit != nil {
						cb.OnCommit()
					}
				}
			}
		})
	})
}
