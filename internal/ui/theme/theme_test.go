package theme

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestRadiiForScalingAndClamping(t *testing.T) {
	cases := []struct {
		name string
		base float32
		want Radii
	}{
		{name: "sharp zero", base: 0, want: Radii{XS: 0, SM: 0, MD: 0, LG: 0}},
		{name: "negative clamps to zero", base: -4, want: Radii{XS: 0, SM: 0, MD: 0, LG: 0}},
		{name: "default six", base: DefaultRadius, want: Radii{XS: 2, SM: 4, MD: 6, LG: 10}},
		{name: "large ten", base: 10, want: Radii{XS: 6, SM: 8, MD: 10, LG: 14}},
		{name: "above max clamps to sixteen", base: 24, want: Radii{XS: 12, SM: 14, MD: 16, LG: 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RadiiFor(tc.base); got != tc.want {
				t.Errorf("RadiiFor(%v) = %+v, want %+v", tc.base, got, tc.want)
			}
		})
	}
}

// TestPaletteMeetsWCAGAA guards against contrast regressions on high-stakes
// buttons, environment badges, cursor cells, and operational metadata.
func TestPaletteMeetsWCAGAA(t *testing.T) {
	for _, dark := range []bool{false, true} {
		p := For(dark).P
		mode := "light"
		if dark {
			mode = "dark"
		}
		pairs := []struct {
			name string
			fg   ui.Color
			bg   ui.Color
		}{
			{"Ink on Canvas", p.Ink, p.Canvas},
			{"InkSoft on Matrix", p.InkSoft, p.Matrix},
			{"InkFaint on Matrix", p.InkFaint, p.Matrix},
			{"InkFaint on Inset", p.InkFaint, p.Inset},
			{"InkGhost on Canvas", p.InkGhost, p.Canvas},
			{"InkGhost on Matrix", p.InkGhost, p.Matrix},
			{"OnKyanite on Kyanite", OnKyanite, p.Kyanite},
			{"KyaniteInk on KyaniteCell", p.KyaniteInk, composite(p.KyaniteCell, p.Canvas)},
			{"OnDanger on OxideFill", OnDanger, p.OxideFill},
			{"Oxide on Matrix", p.Oxide, p.Matrix},
			{"AmberInk on Canvas", p.AmberInk, p.Canvas},
			{"Jade on Canvas", p.Jade, p.Canvas},
		}
		for _, pair := range pairs {
			ratio := contrastRatio(pair.fg, pair.bg)
			if ratio < 4.5 {
				t.Errorf("%s %s: contrast %.2f:1 < 4.5:1", mode, pair.name, ratio)
			}
		}
	}
}

func composite(fg, bg ui.Color) ui.Color {
	a := float64(fg.A) / 255
	blend := func(f, b uint8) uint8 {
		return uint8(math.Round(float64(f)*a + float64(b)*(1-a)))
	}
	return ui.Color{R: blend(fg.R, bg.R), G: blend(fg.G, bg.G), B: blend(fg.B, bg.B), A: 255}
}

func luminance(c ui.Color) float64 {
	ch := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

func contrastRatio(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
