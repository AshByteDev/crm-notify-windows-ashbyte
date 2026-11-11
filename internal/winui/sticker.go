package winui

import (
	"image"
	"image/color"
)

// The sticker of a card (contract 1.11): stickerPx square under the title, in place of the text. The picture of the CRM
// (192 px, transparent) is fitted into the square here, on the white of the card, so GDI only copies the pixels.

const (
	stickerPx    = 96
	stickerTop   = 36
	stickerShift = stickerTop + stickerPx + 4 - 70 // how much lower all below the text goes
	stickerBody  = "Стикер"                        // the body of a sticker card: not repeated next to the picture
)

// shiftFor is how much lower the rest of a card goes when it shows a picture.
func shiftFor(pic bool) int32 {
	if pic {
		return stickerShift
	}
	return 0
}

// fitted draws src into size×size on white: the whole picture, its proportions kept, in the middle; each pixel is the
// average of the pixels of src it covers, so a smaller picture stays smooth.
func fitted(src image.Image, size int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	for i := range out.Pix {
		out.Pix[i] = 0xFF
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 || size <= 0 {
		return out
	}
	w, h := size, size
	if sw > sh {
		h = max(1, size*sh/sw)
	} else if sh > sw {
		w = max(1, size*sw/sh)
	}
	ox, oy := (size-w)/2, (size-h)/2
	for y := range h {
		y0, y1 := b.Min.Y+y*sh/h, b.Min.Y+max((y+1)*sh/h, y*sh/h+1)
		for x := range w {
			x0, x1 := b.Min.X+x*sw/w, b.Min.X+max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					c := color.NRGBA64Model.Convert(src.At(sx, sy)).(color.NRGBA64)
					a := uint64(c.A)
					// over white: c·a + 1·(1−a), in 16 bits
					r += (uint64(c.R)*a + 0xFFFF*(0xFFFF-a)) / 0xFFFF
					g += (uint64(c.G)*a + 0xFFFF*(0xFFFF-a)) / 0xFFFF
					bl += (uint64(c.B)*a + 0xFFFF*(0xFFFF-a)) / 0xFFFF
					n++
				}
			}
			i := out.PixOffset(ox+x, oy+y)
			out.Pix[i], out.Pix[i+1], out.Pix[i+2] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8)
		}
	}
	return out
}
