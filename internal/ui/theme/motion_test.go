package theme

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionTokenBudgetsAndCurves(t *testing.T) {
	// Keep all interactive desktop animations brisk (<= 220ms) and avoid
	// zero-scale pop-ins.
	const maxDuration = 220 * time.Millisecond

	for name, d := range map[string]time.Duration{
		"HoverColor":         HoverColor.Duration,
		"PressAnim":          PressAnim.Duration,
		"PopoverEnter":       PopoverTransition.Duration,
		"PopoverExit":        PopoverTransition.ExitDuration,
		"ModalBackdropEnter": ModalBackdropTransition.Duration,
		"ModalBackdropExit":  ModalBackdropTransition.ExitDuration,
		"ModalPanelEnter":    ModalPanelTransition.Duration,
		"ModalPanelExit":     ModalPanelTransition.ExitDuration,
		"BarRevealEnter":     BarRevealTransition.Duration,
		"BarRevealExit":      BarRevealTransition.ExitDuration,
		"GlideMatch":         GlideMatch("k").Duration,
	} {
		if d <= 0 || d > maxDuration {
			t.Errorf("%s duration %v out of budget (0, %v]", name, d, maxDuration)
		}
	}

	if PopoverTransition.Enter.Scale < 0.95 || ModalPanelTransition.Enter.Scale < 0.95 {
		t.Errorf("overlay enter scale too dramatic: popover=%v modal=%v",
			PopoverTransition.Enter.Scale, ModalPanelTransition.Enter.Scale)
	}
	if PressScale <= 0.94 || PressScale >= 1.0 {
		t.Errorf("PressScale=%v should be subtle in (0.94, 1.0)", PressScale)
	}

	// EaseOut must accelerate early (value at t=0.25 well above linear 0.25).
	if v := EaseOut(0.25); v <= 0.50 {
		t.Errorf("EaseOut(0.25) = %v, expected crisp early acceleration (> 0.50)", v)
	}
}

func TestMotionSettlesAndHonorsReduceMotion(t *testing.T) {
	showBar := false
	activeTab := 0

	view := func(c *ui.Context) {
		k := For(true)
		c.SetTheme(k.UI)
		ui.Column(c).Fill().Background(k.P.Canvas).Children(func() {
			ui.Row(c).Height(34).Children(func() {
				for i := range 2 {
					tab := ui.Box(c.Key(i)).Width(100).Height(34)
					tab.Children(func() {
						if i == activeTab {
							ui.Box(c.Key("edge")).Absolute().Top(0).Left(0).Right(0).Height(2).
								Background(k.P.Kyanite).Transition(GlideMatch("test-edge"))
						}
					})
				}
			})
			if showBar {
				ui.Row(c.Key("bar")).Height(34).Background(k.P.Matrix).
					Transition(BarRevealTransition).Children(func() {
					ui.Text(c, "1 pending")
				})
			}
		})
	}

	// Standard motion: transition starts active and settles within 250ms.
	tt := ui.NewTester(view, 400, 200)
	tt.Frame()
	showBar = true
	activeTab = 1
	tt.Frame()
	tt.Advance(250 * time.Millisecond)
	if !tt.HasText("1 pending") {
		t.Fatal("expected bar text visible after BarRevealTransition settles")
	}

	// ReduceMotion: transitions complete cleanly without hanging or breaking layout.
	tt2 := ui.NewTester(view, 400, 200)
	tt2.SetPreferences(ui.Preferences{ReduceMotion: true})
	showBar = false
	activeTab = 0
	tt2.Frame()
	showBar = true
	activeTab = 1
	tt2.Frame()
	tt2.Advance(250 * time.Millisecond)
	if !tt2.HasText("1 pending") {
		t.Fatal("expected bar text visible under ReduceMotion")
	}
}
