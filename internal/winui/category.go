package winui

import (
	"bytes"
	"embed"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// notify/<category>.png are the icons of the CRM (GET <CRM>/icons/notify/<category>.png, contract 1.13 §5.3.1): a white
// lucide shape on a circle of the colour of the category, 192×192.
//
//go:embed notify/*.png
var categoryFiles embed.FS

// category is where a card comes from: its name (the hint of the icon) and its colour.
type category struct {
	name  string
	color color.RGBA
}

var (
	catCalls     = rgba(0x2E, 0xAD, 0x5B)
	catClients   = rgba(0x33, 0x90, 0xEC)
	catColleague = rgba(0x8E, 0x5C, 0xD9)
	catProcess   = rgba(0x0E, 0x8F, 0x86)
	catAlert     = rgba(0xD9, 0x3B, 0x30)
)

var categories = map[string]category{
	"call_missed":           {"Пропущенный звонок", catAlert}, // red since 1.14, as a missed one in «Звонки»
	"call_incoming":         {"Звонок", catCalls},
	"queue":                 {"Клиент в очереди", catClients},
	"client_message":        {"Сообщение клиента", catClients},
	"colleague_direct":      {"Личное сообщение", catColleague},
	"group_message":         {"Группа", catColleague},
	"mention":               {"Упоминание", catColleague},
	"conversation_assigned": {"Беседу передали вам", catProcess},
	"task":                  {"Задача", catProcess},
	"calendar":              {"Календарь", catProcess},
	"policy":                {"Оргполитика", catProcess},
	"delivery":              {"Доставка", catProcess},
	"alert":                 {"Системное предупреждение", catAlert},
}

// categoryOf: the category of a card; false — none or unknown, the card looks as before 1.13.
func categoryOf(name string) (category, bool) {
	c, ok := categories[name]
	return c, ok
}

// stripDots: at most this many points of colour before the number of the strip.
const stripDots = 4

// stripCategories are the categories the strip shows as points of their colour: the known ones, newest first.
func stripCategories(hidden []string) []string {
	var out []string
	for _, c := range hidden {
		if _, ok := categoryOf(c); ok && len(out) < stripDots {
			out = append(out, c)
		}
	}
	return out
}

// disk is a smooth point of colour c on bg, size×size.
func disk(size int, c, bg color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	r := float64(size) / 2
	for y := range size {
		for x := range size {
			d := math.Hypot(float64(x)+0.5-r, float64(y)+0.5-r)
			img.SetRGBA(x, y, mix(bg, c, clamp(r-d+0.5)))
		}
	}
	return img
}

var (
	iconsOnce sync.Once
	icons     map[string]image.Image
)

// categoryIcon is the icon of a category fitted to size×size on the white of the card; nil — no icon.
func categoryIcon(name string, size int) *image.RGBA {
	iconsOnce.Do(func() {
		icons = map[string]image.Image{}
		for k := range categories {
			b, err := categoryFiles.ReadFile("notify/" + k + ".png")
			if err != nil {
				continue
			}
			if img, err := png.Decode(bytes.NewReader(b)); err == nil {
				icons[k] = img
			}
		}
	})
	img, ok := icons[name]
	if !ok {
		return nil
	}
	return fitted(img, size)
}
