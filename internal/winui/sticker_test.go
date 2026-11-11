package winui

import (
	"image"
	"image/color"
	"testing"
)

func TestFitted(t *testing.T) {
	// a wide picture: a red left half, a transparent right half
	src := image.NewNRGBA(image.Rect(0, 0, 192, 96))
	for y := range 96 {
		for x := range 96 {
			src.SetNRGBA(x, y, color.NRGBA{0xFF, 0, 0, 0xFF})
		}
	}
	out := fitted(src, 96)
	at := func(x, y int) color.RGBA { return out.RGBAAt(x, y) }
	if c := at(10, 48); c != (color.RGBA{0xFF, 0, 0, 0xFF}) {
		t.Errorf("left half %v, want red", c)
	}
	if c := at(80, 48); c != (color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("transparent half %v, want white", c)
	}
	if c := at(10, 5); c != (color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}) {
		t.Errorf("above a wide picture %v, want white: it is centred", c)
	}
	if c := at(10, 30); c.G != 0 {
		t.Errorf("inside the picture %v, want red", c)
	}
	if stickerShift <= 0 || shiftFor(false) != 0 || shiftFor(true) != stickerShift {
		t.Fatal("shift")
	}
}
