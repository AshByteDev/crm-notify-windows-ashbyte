package winui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// Icon colors of the tray by state.
var (
	ColorOnline  = color.RGBA{0x33, 0x90, 0xEC, 0xFF} // connected
	ColorOffline = color.RGBA{0x9A, 0xA4, 0xAF, 0xFF} // no connection / not signed in
	ColorDND     = color.RGBA{0xF0, 0x9A, 0x1A, 0xFF} // «Не беспокоить»
)

// IconPNG draws the tray icon: a round badge with a white bell and, when there are cards, a red dot.
func IconPNG(size int, bg color.RGBA, dot bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	white := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}
	red := color.RGBA{0xE5, 0x3A, 0x35, 0xFF}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+0.5)/s, (float64(y)+0.5)/s
			var c color.RGBA
			// the badge, anti-aliased edge
			if a := cover(math.Hypot(px-0.5, py-0.5), 0.47, 1.2/s); a > 0 {
				c = mix(color.RGBA{}, bg, a)
			}
			// the bell: a dome, a rim and a clapper
			if bell(px, py) {
				c = white
			}
			if dot {
				if a := cover(math.Hypot(px-0.78, py-0.22), 0.2, 1.2/s); a > 0 {
					c = mix(c, red, a)
				}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func bell(x, y float64) bool {
	dome := y > 0.26 && y < 0.66 && math.Abs(x-0.5) < 0.09+0.16*math.Sqrt(math.Max(0, (y-0.26)/0.4))
	rim := y >= 0.62 && y < 0.7 && math.Abs(x-0.5) < 0.27
	clapper := math.Hypot(x-0.5, y-0.76) < 0.07
	knob := math.Hypot(x-0.5, y-0.24) < 0.05
	return dome || rim || clapper || knob
}

// cover is the share of a pixel inside a circle of radius r (soft edge of width w).
func cover(d, r, w float64) float64 {
	return math.Max(0, math.Min(1, (r-d)/w+0.5))
}

func mix(a, b color.RGBA, t float64) color.RGBA {
	l := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}
