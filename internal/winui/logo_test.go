package winui

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestLogo(t *testing.T) {
	b := LogoPNG(32, LogoOptions{Large: true})
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 32 {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("the corner is transparent")
	}
	if r, g, bl, a := img.At(4, 28).RGBA(); a>>8 != 0xFF || bl <= r || bl <= g {
		t.Errorf("the background is navy: %x %x %x %x", r>>8, g>>8, bl>>8, a>>8)
	}
	if r, _, _, _ := img.At(13, 12).RGBA(); r>>8 < 0xB0 {
		t.Errorf("the A is silver: %x", r>>8)
	}
	grey := Logo(32, LogoOptions{Large: true, Offline: true}).RGBAAt(4, 28)
	if grey.R != grey.G || grey.G != grey.B {
		t.Errorf("offline is grey: %v", grey)
	}
	if c := Logo(32, LogoOptions{Unread: true}).RGBAAt(25, 7); c.R < 0xC0 || c.G > 0x80 {
		t.Errorf("the red dot: %v", c)
	}
	if c := Logo(32, LogoOptions{DND: true}).RGBAAt(25, 25); c.R < 0xC0 || c.B > 0x60 {
		t.Errorf("the orange dot: %v", c)
	}
}

// make icon writes the sign for the icon of the .exe: LOGO_OUT=…png
func TestWriteLogo(t *testing.T) {
	out := os.Getenv("LOGO_OUT")
	if out == "" {
		t.Skip()
	}
	if err := os.WriteFile(out, LogoPNG(256, LogoOptions{}), 0o644); err != nil {
		t.Fatal(err)
	}
}
