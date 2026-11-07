package winui

import (
	"image/color"
	"testing"
)

// A circle is round and smooth: the corners stay the card, the centre is the fill, the edge has half tones.
func TestDotImage(t *testing.T) {
	white := rgba(0xFF, 0xFF, 0xFF)
	for _, size := range []int{16, 20, 32} {
		img := dotImage(size, dotRed, "✕", false, white)
		if img.RGBAAt(0, 0) != white || img.RGBAAt(size-1, size-1) != white {
			t.Fatalf("%d: corners %v", size, img.RGBAAt(0, 0))
		}
		edge := map[color.RGBA]bool{}
		for x := 0; x < size; x++ {
			c := img.RGBAAt(x, size/2-size/4)
			if c != white {
				edge[c] = true
			}
		}
		if len(edge) < 3 {
			t.Fatalf("%d: no smooth edge %v", size, edge)
		}
		// symmetric: the left and the right half are mirrors
		for y := 0; y < size; y++ {
			for x := 0; x < size/2; x++ {
				a, b := img.RGBAAt(x, y), img.RGBAAt(size-1-x, y)
				if d := int(a.R) - int(b.R); d > 2 || d < -2 {
					t.Fatalf("%d: (%d,%d) %v vs %v", size, x, y, a, b)
				}
			}
		}
	}
	plain := dotImage(20, dotGreen, "", false, rgba(0xFF, 0xFF, 0xFF))
	if c := plain.RGBAAt(10, 10); c != dotGreen.fill {
		t.Fatalf("fill %v", c)
	}
	if c := dotImage(20, dotGreen, "", true, rgba(0xFF, 0xFF, 0xFF)).RGBAAt(10, 10); c == dotGreen.fill {
		t.Fatal("hover keeps the fill")
	}
	if c := dotImage(20, dotGreen, "✓", false, rgba(0xFF, 0xFF, 0xFF)); c.RGBAAt(10, 10) == plain.RGBAAt(10, 10) && c.RGBAAt(8, 12) == plain.RGBAAt(8, 12) {
		t.Fatal("no sign")
	}
}
