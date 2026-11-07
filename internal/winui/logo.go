package winui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
)

// The sign of AshCRM: the silver folded A on a navy rounded square, from the icons of the CRM (favicon.svg of the set
// «ashcrm-icons-signature», the variant without «crm»: the small word cannot be read at 16–32 px). It is drawn here from
// its outlines, so it is sharp at any DPI: the tray icon, the icons of the windows and the logo in them.

// The colours of the CRM.
var (
	navyTop    = rgba(0x24, 0x45, 0x74)
	navyMid    = rgba(0x19, 0x37, 0x5F)
	navyBottom = rgba(0x14, 0x2D, 0x50) // the background of the CRM splash
	ashLight   = rgba(0xD8, 0xDC, 0xE2)
	ashDark    = rgba(0x8A, 0x93, 0x9F)
	silver0    = rgba(0xFA, 0xFB, 0xFC)
	silver1    = rgba(0xDA, 0xDD, 0xE2)
	silver2    = rgba(0x9D, 0xA4, 0xAF)
	unreadRed  = rgba(0xFF, 0x45, 0x3A)
	dndOrange  = rgba(0xFF, 0x9F, 0x0A)
)

// LogoOptions changes the sign for the tray.
type LogoOptions struct {
	Large   bool // the A bigger than in the CRM icon: the tray icon is 16–32 px and must read at a glance
	Offline bool // grey and paler: no connection or not signed in
	Unread  bool // a red dot at the top right: there are unread cards
	DND     bool // an orange dot at the bottom right: «Не беспокоить»
}

// the outlines in the box 512×512 of the SVG
type pt struct{ x, y float64 }

var (
	// M136 352 222 164 Q228 150 244 150 H280 L192 352 Z
	ribbon = append(append([]pt{{136, 352}, {222, 164}}, quad(pt{222, 164}, pt{228, 150}, pt{244, 150}, 8)...), pt{280, 150}, pt{192, 352})
	// M287 180 376 352 H317 L291 301 H224 L246 253 H267 L259 237 Z
	fold = []pt{{287, 180}, {376, 352}, {317, 352}, {291, 301}, {224, 301}, {246, 253}, {267, 253}, {259, 237}}
)

func quad(a, c, b pt, n int) []pt {
	out := make([]pt, 0, n)
	for i := 1; i <= n; i++ {
		t := float64(i) / float64(n)
		u := 1 - t
		out = append(out, pt{u*u*a.x + 2*u*t*c.x + t*t*b.x, u*u*a.y + 2*u*t*c.y + t*t*b.y})
	}
	return out
}

// Logo draws the sign size×size with transparent corners.
func Logo(size int, o LogoOptions) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := 1.15 // as in the CRM icon
	if o.Large {
		scale = 1.6
	}
	n := 4 // samples per side of a pixel
	if size <= 48 {
		n = 8
	}
	k := 512 / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, b, a float64
			for sy := 0; sy < n; sy++ {
				for sx := 0; sx < n; sx++ {
					X := (float64(x) + (float64(sx)+0.5)/float64(n)) * k
					Y := (float64(y) + (float64(sy)+0.5)/float64(n)) * k
					c, ok := logoAt(X, Y, scale)
					if !ok {
						continue
					}
					r, g, b, a = r+float64(c.R), g+float64(c.G), b+float64(c.B), a+1
				}
			}
			if a == 0 {
				continue
			}
			cov := a / float64(n*n)
			c := color.RGBA{uint8(r / a), uint8(g / a), uint8(b / a), 0xFF}
			if o.Offline {
				l := uint8(0.3*float64(c.R) + 0.59*float64(c.G) + 0.11*float64(c.B))
				c = color.RGBA{l, l, l, 0xFF}
				cov *= 0.8
			}
			img.SetRGBA(x, y, premul(c, cov))
		}
	}
	if o.Unread {
		badge(img, 0.78, 0.22, unreadRed)
	}
	if o.DND {
		badge(img, 0.78, 0.78, dndOrange)
	}
	return img
}

// logoAt is the colour of a point of the 512 box; false — outside the rounded square.
func logoAt(x, y, scale float64) (color.RGBA, bool) {
	if !inRounded(x, y, 512, 112) {
		return color.RGBA{}, false
	}
	// the A, scaled around the centre
	ax, ay := 256+(x-256)/scale, 256+(y-256)/scale
	if inPoly(ribbon, ax, ay) {
		t := grad(ax, ay, 136, 150, 280, 352, 0.8, 1)
		return stops(t, silver0, silver1, silver2), true
	}
	if inPoly(fold, ax, ay) {
		return mix(ashLight, ashDark, grad(ax, ay, 224, 180, 376, 352, 1, 1)), true
	}
	c := stops((x+y)/1024, navyTop, navyMid, navyBottom)
	if !inRounded(x-2, y-2, 508, 110) { // the thin light edge of the CRM icon
		c = mix(c, rgba(0xFF, 0xFF, 0xFF), 0.10)
	}
	return c, true
}

// grad is the place of a point along a gradient of the box (x0,y0)–(x1,y1) in the direction (dx, dy) of the box.
func grad(x, y, x0, y0, x1, y1, dx, dy float64) float64 {
	u, v := (x-x0)/(x1-x0), (y-y0)/(y1-y0)
	return clamp((u*dx + v*dy) / (dx*dx + dy*dy))
}

// stops: the three colours at 0, 0.55 and 1.
func stops(t float64, a, b, c color.RGBA) color.RGBA {
	if t < 0.55 {
		return mix(a, b, t/0.55)
	}
	return mix(b, c, (t-0.55)/0.45)
}

func inRounded(x, y, side, r float64) bool {
	if x < 0 || y < 0 || x > side || y > side {
		return false
	}
	cx, cy := math.Max(r-x, math.Max(0, x-(side-r))), math.Max(r-y, math.Max(0, y-(side-r)))
	if cx <= 0 || cy <= 0 {
		return true
	}
	return cx*cx+cy*cy <= r*r
}

func inPoly(p []pt, x, y float64) bool {
	in := false
	for i, j := 0, len(p)-1; i < len(p); j, i = i, i+1 {
		if (p[i].y > y) != (p[j].y > y) && x < (p[j].x-p[i].x)*(y-p[i].y)/(p[j].y-p[i].y)+p[i].x {
			in = !in
		}
	}
	return in
}

func premul(c color.RGBA, a float64) color.RGBA {
	return color.RGBA{uint8(float64(c.R) * a), uint8(float64(c.G) * a), uint8(float64(c.B) * a), uint8(255 * a)}
}

// badge puts a dot at (cx, cy) of the size with a transparent gap around it, so it stands apart from the sign.
func badge(img *image.RGBA, cx, cy float64, c color.RGBA) {
	s := float64(img.Bounds().Dx())
	r, gap := 0.18*s, 0.26*s
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			d := math.Hypot(float64(x)+0.5-cx*s, float64(y)+0.5-cy*s)
			keep := clamp(d - gap + 0.5) // the gap clears the sign
			o := img.RGBAAt(x, y)
			o = color.RGBA{uint8(float64(o.R) * keep), uint8(float64(o.G) * keep), uint8(float64(o.B) * keep), uint8(float64(o.A) * keep)}
			if a := clamp(r - d + 0.5); a > 0 {
				dot := premul(c, a)
				o = color.RGBA{dot.R + uint8(float64(o.R)*(1-a)), dot.G + uint8(float64(o.G)*(1-a)), dot.B + uint8(float64(o.B)*(1-a)), dot.A + uint8(float64(o.A)*(1-a))}
			}
			img.SetRGBA(x, y, o)
		}
	}
}

// LogoPNG is the sign as a PNG (the tray and the windows take icons from PNG).
func LogoPNG(size int, o LogoOptions) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, unpremul(Logo(size, o)))
	return b.Bytes()
}

// unpremul turns the premultiplied pixels of image.RGBA into the straight ones of NRGBA for PNG.
func unpremul(img *image.RGBA) *image.NRGBA {
	out := image.NewNRGBA(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			out.Set(x, y, img.RGBAAt(x, y))
		}
	}
	return out
}

// onto is the sign laid over a solid colour (a window paints it without transparency).
func onto(img *image.RGBA, bg color.RGBA) *image.RGBA {
	out := image.NewRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		a := float64(img.Pix[i+3]) / 255
		for j := 0; j < 3; j++ {
			out.Pix[i+j] = img.Pix[i+j] + uint8(float64([]uint8{bg.R, bg.G, bg.B}[j])*(1-a))
		}
		out.Pix[i+3] = 0xFF
	}
	return out
}
