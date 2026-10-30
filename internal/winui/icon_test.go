package winui

import (
	"bytes"
	"image/png"
	"os"
	"testing"
)

func TestIconPNG(t *testing.T) {
	b := IconPNG(32, ColorOnline, true)
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil || img.Bounds().Dx() != 32 {
		t.Fatal(err)
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("the corner is transparent")
	}
	if r, g, bl, _ := img.At(16, 13).RGBA(); r>>8 != 0xFF || g>>8 != 0xFF || bl>>8 != 0xFF {
		t.Error("the bell is white")
	}
	if dir := os.Getenv("ICON_OUT"); dir != "" { // look at them: ICON_OUT=/tmp go test ./internal/winui/
		_ = os.WriteFile(dir+"/icon-online.png", IconPNG(64, ColorOnline, true), 0o644)
		_ = os.WriteFile(dir+"/icon-offline.png", IconPNG(64, ColorOffline, false), 0o644)
		_ = os.WriteFile(dir+"/icon-dnd.png", IconPNG(64, ColorDND, false), 0o644)
	}
}
