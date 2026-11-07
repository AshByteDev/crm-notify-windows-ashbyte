package winui

import (
	"image"
	"image/color"
	"math"
)

// The circles of a card are drawn here, not by GDI: GDI has no smoothing, its small circles come out ragged. A circle is
// a flat fill with a thin darker ring and a sign of strokes with round ends, like the buttons of a window on macOS; the
// edges are anti-aliased against the white of the card.

// dotStyle is the colours of one circle.
type dotStyle struct{ fill, ring, sign color.RGBA }

var (
	dotGreen = dotStyle{rgba(0x28, 0xC8, 0x40), rgba(0x1D, 0xAD, 0x2B), rgba(0x0B, 0x5E, 0x15)}
	dotBlue  = dotStyle{rgba(0x3B, 0x9B, 0xFF), rgba(0x2A, 0x84, 0xE5), rgba(0x0B, 0x3F, 0x80)}
	dotRed   = dotStyle{rgba(0xFF, 0x5F, 0x57), rgba(0xE2, 0x46, 0x3F), rgba(0x8E, 0x1F, 0x17)}
)

func rgba(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 0xFF} }

// the signs, in a circle of radius 1 around (0, 0), y down
type segment struct{ ax, ay, bx, by float64 }

var signs = map[string][]segment{
	"✓": {{-0.38, 0.02, -0.11, 0.29}, {-0.11, 0.29, 0.38, -0.25}},
	"↗": {{-0.30, 0.30, 0.28, -0.28}, {-0.08, -0.30, 0.30, -0.30}, {0.30, -0.30, 0.30, 0.08}},
	"✕": {{-0.30, -0.30, 0.30, 0.30}, {-0.30, 0.30, 0.30, -0.30}},
}

// dotImage draws a circle of size×size pixels on bg. hover makes the fill a little deeper and the sign clearer.
func dotImage(size int, st dotStyle, sign string, hover bool, bg color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	r := float64(size) / 2
	ring := math.Max(1, r/12) // the width of the ring, px
	half := math.Max(0.8, r*0.12)
	fill, signAlpha := st.fill, 0.62
	if hover {
		fill, signAlpha = mix(st.fill, st.ring, 0.45), 0.95
	}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := float64(x)+0.5-r, float64(y)+0.5-r
			d := math.Hypot(px, py)
			c := bg
			if a := clamp(r - d + 0.5); a > 0 {
				body := mix(st.ring, fill, clamp(r-ring-d+0.5)) // the ring at the edge, the fill inside
				if k := strokes(signs[sign], px/r, py/r) * r; k < half+1 {
					body = mix(body, st.sign, clamp(half-k+0.5)*signAlpha)
				}
				c = mix(bg, body, a)
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// strokes is the distance from a point to the nearest segment, in radii.
func strokes(s []segment, x, y float64) float64 {
	best := math.Inf(1)
	for _, g := range s {
		dx, dy := g.bx-g.ax, g.by-g.ay
		t := clamp(((x-g.ax)*dx + (y-g.ay)*dy) / (dx*dx + dy*dy))
		best = math.Min(best, math.Hypot(x-g.ax-t*dx, y-g.ay-t*dy))
	}
	return best
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// bgra is the pixels of an image in the order of a 32-bit DIB of Windows.
func bgra(img *image.RGBA) []byte {
	out := make([]byte, len(img.Pix))
	for i := 0; i < len(img.Pix); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = img.Pix[i+2], img.Pix[i+1], img.Pix[i], 0xFF
	}
	return out
}
