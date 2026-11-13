package winui

import "testing"

// Every call_kind of contract 1.14 §5.3.2 has its handset in its colour; an unknown one is grey, no field — none.
func TestHandsets(t *testing.T) {
	near := func(a, b uint8) bool { return int(a)-int(b) < 40 && int(b)-int(a) < 40 }
	kinds := []string{"in", "out", "missed", "dropped", "spam", "ringing", "fax"}
	for _, k := range kinds {
		h, _ := handsetOf(k)
		img := handset(k, 48)
		if img == nil {
			t.Fatalf("%s: no handset", k)
		}
		if img.RGBAAt(0, 47) != rgba(0xFF, 0xFF, 0xFF) && img.RGBAAt(47, 47) != rgba(0xFF, 0xFF, 0xFF) {
			t.Errorf("%s: no white of the card", k)
		}
		found := false // a pixel of the shape in the colour of the kind
		for y := range 48 {
			for x := range 48 {
				c := img.RGBAAt(x, y)
				if near(c.R, h.color.R) && near(c.G, h.color.G) && near(c.B, h.color.B) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no pixel of %v", k, h.color)
		}
	}
	if h, _ := handsetOf("fax"); h != handsetGrey {
		t.Fatal("an unknown kind is not grey")
	}
	if handset("", 16) != nil {
		t.Fatal("a handset for no call")
	}
}
