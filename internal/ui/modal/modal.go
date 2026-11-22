// Package modal implements KyanDB's floating dialogs (Z > 0): native
// desktop two-column connection configuration and Review SQL diff approval.
package modal

import (
	"github.com/egoist/mygo/ui"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
	"github.com/AnalyseDeCircuit/kyandb/internal/ui/theme"
	"github.com/AnalyseDeCircuit/kyandb/locales"
)

// labelColWidth aligns every desktop form label along one clean vertical axis.
const labelColWidth = 124

func msgs(c *ui.Context) locales.Messages { return locales.In(c.Locale()) }

func seam(c *ui.Context, color ui.Color) {
	ui.Box(c).Height(1).FillWidth().Background(color)
}

// frame wraps ui.DialogBase with KyanDB's Z > 0 overlay elevation,
// continuous superellipse LG corners, and backdrop dimming.
func frame(c *ui.Context, k theme.Set, open *bool, width float32, onDismiss func(), body func()) {
	p := k.P
	dim := ui.RGBA(0, 0, 0, 0.36)
	if p.Dark {
		dim = ui.RGBA(0, 0, 0, 0.56)
	}
	panel := ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		backdrop.Background(dim).Transition(theme.ModalBackdropTransition)
		panel.Width(width).MaxWidthPercent(92).Radius(k.R.LG).
			Background(p.Matrix).Border(1, p.SeamStrong).
			Shadow(0, 16, 40, 0, ui.RGBA(0, 0, 0, 0.32)).
			TransformOrigin(0.5, 0.5).Transition(theme.ModalPanelTransition).
			Clip().Children(body)
	})
	if panel.Valid() && panel.Dismissed() && onDismiss != nil {
		onDismiss()
	}
}

// header renders a clean 42 DIP modal header without pill containers.
func header(c *ui.Context, k theme.Set, title, subtitle string, env domain.Environment, showEnv bool, onClose func()) {
	p := k.P
	if showEnv && env.Protected() {
		ui.Box(c).Height(2).FillWidth().Background(p.Env(env).Alpha(0.85))
	}
	ui.Row(c).Height(42).PaddingX(16).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(13.5).FontWeight(600).TextColor(p.Ink).SingleLine()
		if showEnv {
			ec := p.Env(env)
			ui.Text(c, "·").TextColor(p.InkGhost)
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(ec)
				if env.Protected() {
					ui.Text(c, msgs(c).EnvTag(string(env))).FontSize(10.5).FontWeight(700).LetterSpacing(0.4).TextColor(ec).SingleLine()
				}
				if subtitle != "" {
					ui.Text(c, subtitle).FontSize(theme.FontSize).FontWeight(500).TextColor(p.InkSoft).SingleLine()
				} else if !env.Protected() {
					ui.Text(c, msgs(c).EnvTag(string(env))).FontSize(10.5).FontWeight(600).TextColor(p.InkFaint).SingleLine()
				}
			})
		}
		ui.Spacer(c)
		closeBtn := ui.ButtonBase(c.Key("modal-close")).
			Size(24, 24).Radius(k.R.SM).Label(msgs(c).ModalClose()).Tooltip(msgs(c).ModalClose())
		bg, cc := ui.Transparent, p.InkFaint
		if closeBtn.Hovered() {
			bg, cc = p.Ink.Alpha(0.06), p.Ink
		}
		closeBtn.Background(closeBtn.AnimateColor("bg", bg, theme.HoverColor))
		theme.ApplyPress(closeBtn, closeBtn.Pressed())
		closeBtn.Children(func() {
			ui.Icon(c, theme.IconClose).Size(12, 12).TextColor(cc)
		})
		if closeBtn.Clicked() && onClose != nil {
			onClose()
		}
	})
	seam(c, p.Seam)
}

type buttonKind int

const (
	buttonGhost buttonKind = iota
	buttonOutline
	buttonPrimary
	buttonDanger
)

func actionButton(c *ui.Context, k theme.Set, id, label string, kind buttonKind, icon *ui.SVG) bool {
	p := k.P
	b := ui.ButtonBase(c.Key(id)).Height(28).PaddingX(12).Gap(5).Radius(k.R.SM)
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
			ui.Icon(c, icon).Size(12, 12).TextColor(fg)
		}
		ui.Text(c, label).FontWeight(500).TextColor(fg).SingleLine()
	})
	return b.Clicked()
}

// formRow aligns a fixed-width desktop label on the left with its control on
// the right, avoiding stacked web-style boxes.
func formRow(c *ui.Context, p *theme.Palette, label string, control func()) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Width(labelColWidth).Justify(ui.End).Children(func() {
			ui.Text(c, label).FontSize(theme.FontSize).TextColor(p.InkSoft).SingleLine().AccessHidden(true)
		})
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(control)
	})
}

// textRow renders one horizontal label + native TextInput row. Clicking the
// label focuses the input.
func textRow(c *ui.Context, k theme.Set, id, label, placeholder string, value *string, mono, secret bool) bool {
	p := k.P
	changed := false
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		lbl := ui.ButtonBase(c.Key(id + "-lbl")).Width(labelColWidth).Justify(ui.End).AccessHidden(true).Children(func() {
			ui.Text(c, label).FontSize(theme.FontSize).TextColor(p.InkSoft).SingleLine()
		})
		in := ui.TextInput(c.Key(id), value).
			Height(28).Padding(5, 8).
			Background(p.Inset).Border(1, p.Seam).Radius(k.R.SM).
			Placeholder(placeholder).Label(label).Grow(1).MinWidth(0)
		if mono {
			in.Font(theme.MonoFamily)
		}
		if secret {
			in.Password()
		}
		if lbl.Clicked() {
			in.Focus()
		}
		changed = in.Changed()
	})
	return changed
}

// sectionTabs renders clean flat underline tabs along the top of a modal
// with a matched-geometry gliding Kyanite underline.
func sectionTabs(c *ui.Context, k theme.Set, selected *int, labels ...string) bool {
	p := k.P
	strip := ui.TabsBase(c, selected, len(labels))
	strip.List.Height(32).PaddingX(16).Gap(16).AlignItems(ui.Stretch).
		BorderWidth(0, 0, 1, 0).BorderColor(p.Seam).Children(func() {
		for i, lbl := range labels {
			active := i == *selected
			tab := strip.Tab(i).AlignItems(ui.Center).PaddingX(2)
			fg := p.InkFaint
			if active {
				fg = p.Ink
			} else if tab.Hovered() {
				fg = p.InkSoft
			}
			tab.Children(func() {
				ui.Text(c, lbl).FontWeight(500).TextColor(fg).SingleLine()
				if active {
					ui.Box(c.Key("modal-sec-edge")).Absolute().Bottom(0).Left(0).Right(0).Height(2).
						Background(p.Kyanite).Transition(theme.GlideMatch("modal-sec-edge"))
				}
			})
		}
	})
	return strip.List.Changed()
}

// segmented renders a quiet native segmented control with a gliding active thumb.
func segmented(c *ui.Context, k theme.Set, id string, selected *int, labels ...string) bool {
	p := k.P
	parts := ui.SegmentedBase(c.Key(id), selected, len(labels))
	parts.Track.Height(26).Padding(2).Gap(2).AlignItems(ui.Stretch).
		Background(p.Inset).Radius(k.R.SM).Grow(1).Children(func() {
		for i, lbl := range labels {
			active := i == *selected
			seg := parts.Segment(i).Grow(1).Center().PaddingX(10).Radius(k.R.XS)
			fg := p.InkFaint
			if active {
				fg = p.Ink
			} else if seg.Hovered() {
				fg = p.InkSoft
			}
			seg.Children(func() {
				if active {
					ui.Box(c.Key("seg-thumb-"+id)).Absolute().Left(0).Top(0).Right(0).Bottom(0).
						Radius(k.R.XS).Background(p.Canvas).Border(1, p.Seam).
						Transition(theme.GlideMatch("seg-" + id))
				}
				ui.Text(c, lbl).FontSize(11.5).FontWeight(500).TextColor(fg).SingleLine()
			})
		}
	})
	return parts.Track.Changed()
}
