package modal

import (
	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
)

// HostKeyCallbacks receives the user's explicit SSH host key trust decision.
type HostKeyCallbacks struct {
	OnTrust  func(save bool)
	OnReject func()
}

// HostKey renders the SSH host key verification dialog when challenge is non-nil.
// Unknown keys and changed (mismatch) keys keep distinct visual risk signals.
func HostKey(c *ui.Context, k theme.Set, challenge *domain.HostKeyChallenge, cb HostKeyCallbacks) {
	if challenge == nil {
		return
	}
	p := k.P
	m := msgs(c)
	open := true

	reject := func() {
		if cb.OnReject != nil {
			cb.OnReject()
		}
	}

	title := m.HostKeyTitle()
	hint := m.HostKeyUnknownHint()
	if challenge.Mismatch {
		title = m.HostKeyMismatchTitle()
		hint = m.HostKeyMismatchHint()
	}

	frame(c, k, &open, 560, reject, func() {
		if challenge.Mismatch {
			ui.Box(c).Height(2).FillWidth().Background(p.Oxide.Alpha(0.85))
		}
		ui.Row(c).Height(42).PaddingX(16).Gap(8).AlignItems(ui.Center).Children(func() {
			if challenge.Mismatch {
				ui.Icon(c, theme.IconAlert).Size(14, 14).Shrink(0).TextColor(p.Oxide)
			}
			ui.Text(c, title).FontSize(13.5).FontWeight(600).TextColor(p.Ink).SingleLine()
			ui.Text(c, "·").TextColor(p.InkGhost)
			ui.Text(c, challenge.Host).Font(theme.MonoFamily).FontSize(theme.SmallSize).TextColor(p.InkSoft).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			ui.Spacer(c)
			closeBtn := ui.ButtonBase(c.Key("hk-close")).
				Size(24, 24).Radius(k.R.SM).Label(m.ModalClose()).Tooltip(m.ModalClose())
			cc := p.InkFaint
			if closeBtn.Hovered() {
				closeBtn.Background(p.Ink.Alpha(0.06))
				cc = p.Ink
			}
			closeBtn.Children(func() {
				ui.Icon(c, theme.IconClose).Size(12, 12).TextColor(cc)
			})
			if closeBtn.Clicked() {
				reject()
			}
		})
		seam(c, p.Seam)

		ui.Column(c).Padding(16, 20, 18, 16).Gap(12).Children(func() {
			if challenge.Mismatch {
				ui.Row(c).Padding(8, 10).Gap(8).Radius(k.R.SM).
					Background(p.DeletedBg).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, theme.IconAlert).Size(14, 14).Shrink(0).TextColor(p.Oxide)
					ui.Text(c, hint).FontSize(theme.SmallSize).TextColor(p.Ink).Grow(1).Shrink(1).MinWidth(0)
				})
			} else {
				ui.Text(c, hint).FontSize(theme.SmallSize).TextColor(p.InkSoft)
			}

			ui.Column(c).Gap(8).Children(func() {
				hostKeyRow(c, p, m.HostKeyFieldHost(), challenge.Host, true)
				hostKeyRow(c, p, m.HostKeyFieldType(), challenge.KeyType, true)
				hostKeyRow(c, p, m.HostKeyFieldFingerprint(), challenge.Fingerprint, true)
				if challenge.Mismatch && challenge.KnownFingerprint != "" {
					hostKeyRow(c, p, m.HostKeyFieldPrevious(), challenge.KnownFingerprint, true)
				}
			})
		})

		seam(c, p.Seam)
		ui.Row(c).Height(46).PaddingX(16).Gap(8).AlignItems(ui.Center).Background(p.Matrix).Children(func() {
			if actionButton(c, k, "hk-cancel", m.ModalCancel(), buttonGhost, nil) {
				reject()
			}
			ui.Spacer(c)
			if actionButton(c, k, "hk-trust-once", m.HostKeyTrustOnce(), buttonOutline, nil) {
				if cb.OnTrust != nil {
					cb.OnTrust(false)
				}
			}
			saveKind := buttonPrimary
			if challenge.Mismatch {
				saveKind = buttonDanger
			}
			if actionButton(c, k, "hk-trust-save", m.HostKeyTrustSave(), saveKind, nil) {
				if cb.OnTrust != nil {
					cb.OnTrust(true)
				}
			}
		})
	})
}

func hostKeyRow(c *ui.Context, p *theme.Palette, label, val string, mono bool) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Width(labelColWidth).Justify(ui.End).Shrink(0).Children(func() {
			ui.Text(c, label).FontSize(theme.SmallSize).TextColor(p.InkSoft).SingleLine()
		})
		t := ui.Text(c, val).FontSize(theme.SmallSize).TextColor(p.Ink).Grow(1).Shrink(1).MinWidth(0)
		if mono {
			t.Font(theme.MonoFamily)
		}
	})
}
