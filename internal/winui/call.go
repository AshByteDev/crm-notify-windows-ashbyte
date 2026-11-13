package winui

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
)

// phone/<lucide>.png are the lucide handsets (the shapes of the CRM, 24 units drawn at 96×96): black strokes on
// transparency, the alpha is the shape; handset tints them in the colour of the kind of the call.
//
//go:embed phone/*.png
var phoneFiles embed.FS

// handsetStyle is the handset of a kind of call (contract 1.14 §5.3.2).
type handsetStyle struct {
	icon  string // lucide
	color color.RGBA
}

var (
	handsetGrey = handsetStyle{"phone-call", rgba(0x8A, 0x8F, 0x98)} // an unknown kind
	handsets    = map[string]handsetStyle{
		"in":      {"phone-incoming", rgba(0x2E, 0xAD, 0x5B)},
		"out":     {"phone-outgoing", rgba(0x33, 0x90, 0xEC)},
		"missed":  {"phone-missed", rgba(0xD9, 0x3B, 0x30)},
		"dropped": {"phone-off", rgba(0x8A, 0x8F, 0x98)},
		"spam":    {"phone-incoming", rgba(0xF9, 0x73, 0x16)},
		"ringing": {"phone-call", rgba(0x2E, 0xAD, 0x5B)},
	}
)

// handsetOf: the handset of call_kind; false — not a call (no field).
func handsetOf(kind string) (handsetStyle, bool) {
	if kind == "" {
		return handsetStyle{}, false
	}
	if h, ok := handsets[kind]; ok {
		return h, true
	}
	return handsetGrey, true
}

var (
	phonesOnce sync.Once
	phones     map[string]image.Image
)

// handset is the handset of call_kind in its colour, size×size on the white of the card; nil — not a call.
func handset(kind string, size int) *image.RGBA {
	h, ok := handsetOf(kind)
	if !ok {
		return nil
	}
	phonesOnce.Do(func() {
		phones = map[string]image.Image{}
		files, _ := phoneFiles.ReadDir("phone")
		for _, f := range files {
			b, err := phoneFiles.ReadFile("phone/" + f.Name())
			if err != nil {
				continue
			}
			if img, err := png.Decode(bytes.NewReader(b)); err == nil {
				phones[strings.TrimSuffix(f.Name(), ".png")] = img
			}
		}
	})
	mask, ok := phones[h.icon]
	if !ok {
		return nil
	}
	b := mask.Bounds()
	tinted := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := mask.At(x, y).RGBA()
			tinted.SetNRGBA(x, y, color.NRGBA{h.color.R, h.color.G, h.color.B, uint8(a >> 8)})
		}
	}
	return fitted(tinted, size)
}
