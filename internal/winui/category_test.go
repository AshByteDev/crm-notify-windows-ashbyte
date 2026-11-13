package winui

import (
	"slices"
	"testing"
)

// Every category of contract 1.13 §5.3.1 has its icon, and the icon is of the colour of its category.
func TestCategoryIcons(t *testing.T) {
	for name, cat := range categories {
		img := categoryIcon(name, 24)
		if img == nil {
			t.Fatalf("%s: no icon", name)
		}
		// a point of the circle away from the white shape
		c := img.RGBAAt(12, 2)
		near := func(a, b uint8) bool { return int(a)-int(b) < 24 && int(b)-int(a) < 24 }
		if !near(c.R, cat.color.R) || !near(c.G, cat.color.G) || !near(c.B, cat.color.B) {
			t.Errorf("%s: icon %v, colour %v", name, c, cat.color)
		}
		if img.RGBAAt(0, 0) != rgba(0xFF, 0xFF, 0xFF) {
			t.Errorf("%s: the corner is not the white of the card", name)
		}
	}
	if categoryIcon("", 24) != nil || categoryIcon("weather", 24) != nil {
		t.Fatal("an icon for no category")
	}
}

func TestStripCategories(t *testing.T) {
	got := stripCategories([]string{"", "queue", "weather", "task", "mention", "alert", "delivery"})
	if want := []string{"queue", "task", "mention", "alert"}; !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
}
