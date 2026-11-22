package theme

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// KyanDB's motion language is disciplined, fast (<= 200 ms), and never
// bouncy: animations exist only to preserve spatial continuity (popovers
// opening from their anchor, modals rising into focus, ledger/error bars
// revealing without snapping the grid, and active indicators gliding across
// tabs and segmented controls). High-frequency workspace actions—data grid
// scrolling, cell selection, keyboard shortcuts, and schema filtering—never
// animate.
//
// All primitives below automatically honor OS Preferences.ReduceMotion via
// mygo/ui: positional/scale/collapse motion snaps immediately while subtle
// color and opacity cues remain smooth.
var (
	// EaseOut is a crisp deceleration curve (fast start, decisive settle)
	// for entrances, popovers, dialogs, and tactile press feedback.
	EaseOut = ui.CubicBezier(0.23, 1, 0.32, 1)

	// EaseOutFast accelerates exits and subtle state transitions.
	EaseOutFast = ui.CubicBezier(0.2, 0, 0, 1)

	// EaseInOut smoothly accelerates and decelerates on-screen spatial glides
	// (active tab top edge, modal section underline, segmented thumbs).
	EaseInOut = ui.CubicBezier(0.77, 0, 0.175, 1)

	// HoverColor blends button and row tints in Oklab space; NonMotion keeps
	// color transitions smooth even when ReduceMotion is enabled.
	HoverColor = ui.Animation{
		Duration:  110 * time.Millisecond,
		Ease:      EaseOut,
		Oklab:     true,
		NonMotion: true,
	}

	// PressAnim drives GPU-composited tactile scale feedback on pointer press.
	PressAnim = ui.Animation{
		Duration: 110 * time.Millisecond,
		Ease:     EaseOut,
	}

	// PopoverTransition scales anchored popovers out of their trigger origin
	// (pair with TransformOrigin(0, 0) for bottom-start popovers).
	PopoverTransition = ui.ElementTransition{
		Duration:     150 * time.Millisecond,
		Ease:         EaseOut,
		ExitDuration: 100 * time.Millisecond,
		ExitEase:     EaseOutFast,
		Colors:       true,
		Enter:        &ui.Motion{Y: -4, Scale: 0.97, Opacity: 0, Flip: true},
		Exit:         &ui.Motion{Y: -3, Scale: 0.98, Opacity: 0, Flip: true},
	}

	// ModalBackdropTransition fades the dialog scrim in and out cleanly.
	ModalBackdropTransition = ui.ElementTransition{
		Duration:     160 * time.Millisecond,
		Ease:         EaseOut,
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     EaseOutFast,
		Colors:       true,
		Enter:        &ui.Motion{Opacity: 0},
		Exit:         &ui.Motion{Opacity: 0},
	}

	// ModalPanelTransition lifts floating dialogs into center focus without
	// exaggerating scale (never scaling from 0) and without clipping dynamic
	// form height changes inside the dialog.
	ModalPanelTransition = ui.ElementTransition{
		Duration:     180 * time.Millisecond,
		Ease:         EaseOut,
		ExitDuration: 120 * time.Millisecond,
		ExitEase:     EaseOutFast,
		Colors:       true,
		Enter:        &ui.Motion{Y: 6, Scale: 0.96, Opacity: 0},
		Exit:         &ui.Motion{Y: 4, Scale: 0.98, Opacity: 0},
	}

	// BarRevealTransition smoothly expands and collapses contextual bars
	// (staged-edit ledger, inline SQL error banners) so the data grid never
	// jumps abruptly.
	BarRevealTransition = ui.ElementTransition{
		Duration:     180 * time.Millisecond,
		Ease:         EaseOut,
		ExitDuration: 130 * time.Millisecond,
		ExitEase:     EaseOutFast,
		Height:       true,
		Enter:        &ui.Motion{Collapse: true, Y: 4, Opacity: 0},
		Exit:         &ui.Motion{Collapse: true, Y: 4, Opacity: 0},
	}
)

// PressScale is the subtle scale factor applied while a button is pressed.
const PressScale float32 = 0.97

// ApplyPress adds GPU-composited press feedback (zero layout cost) to an
// interactive control.
func ApplyPress(el ui.Element, pressed bool) {
	if pressed {
		el.AnimatePose(ui.Pose{Scale: PressScale}, PressAnim)
		return
	}
	el.AnimatePose(ui.Pose{}, PressAnim)
}

// GlideMatch returns a matched-geometry transition that glides an active
// indicator (tab edge, section underline, segmented pill) from its previous
// sibling position and width to its new one.
func GlideMatch(matchKey any) ui.ElementTransition {
	return ui.ElementTransition{
		Duration: 170 * time.Millisecond,
		Ease:     EaseInOut,
		Position: true,
		Size:     true,
		Match:    matchKey,
	}
}
