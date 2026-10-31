package winui

import (
	"fmt"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/queue"
)

// The geometry and the texts of the cards and of the picture in the settings: plain logic, tested on any system.

type point struct{ X, Y int32 }

type rect struct{ Left, Top, Right, Bottom int32 }

func in(r rect, x, y int32) bool { return x >= r.Left && x < r.Right && y >= r.Top && y < r.Bottom }

// stack places windows of one width in an area: the first one at the edge of the chosen place, the rest going towards
// the middle of the screen.
func stack(position string, area rect, width int32, heights []int32, margin, gap int32) []point {
	var x int32
	switch position {
	case app.TopLeft, app.BottomLeft:
		x = area.Left + margin
	case app.TopCenter, app.BottomCenter:
		x = area.Left + (area.Right-area.Left-width)/2
	default:
		x = area.Right - margin - width
	}
	top := position == app.TopLeft || position == app.TopCenter || position == app.TopRight
	out := make([]point, len(heights))
	y := area.Top + margin
	if !top {
		y = area.Bottom - margin
	}
	for i, h := range heights {
		if top {
			out[i] = point{x, y}
			y += h + gap
		} else {
			y -= h
			out[i] = point{x, y}
			y -= gap
		}
	}
	return out
}

// clock is the time of a message on this computer: hours and minutes, no date.
func clock(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("15:04")
}

// cardTitle is who wrote, with the number of unread messages when there are several.
func cardTitle(c api.Card) string {
	if c.Count > 1 {
		return fmt.Sprintf("%s (%d)", c.Title, c.Count)
	}
	return c.Title
}

// stripText is the strip at the edge of the screen ("" — nothing unread, no strip).
func stripText(q queue.View) string {
	if q.Unread == 0 {
		return ""
	}
	if q.Browsing {
		return fmt.Sprintf("%d–%d из %d · колесо мыши листает", q.Offset+1, q.Offset+len(q.Visible), q.Listed)
	}
	t := fmt.Sprintf("%d %s", q.Unread, plural(q.Unread, "уведомление", "уведомления", "уведомлений"))
	if q.DND {
		t += " · не беспокоить"
	}
	return t
}

func plural(n int, one, few, many string) string {
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// zone is a clickable part of the screen in the picture.
type zone struct {
	position, name string
	box            rect
}

// zones divides the picture of the screen into six parts: three above, three below.
func zones(screen rect) []zone {
	w, h := screen.Right-screen.Left, screen.Bottom-screen.Top
	cell := func(col, row int32) rect {
		return rect{screen.Left + w*col/3, screen.Top + h*row/2, screen.Left + w*(col+1)/3, screen.Top + h*(row+1)/2}
	}
	return []zone{
		{app.TopLeft, "слева вверху", cell(0, 0)},
		{app.TopCenter, "вверху по центру", cell(1, 0)},
		{app.TopRight, "справа вверху", cell(2, 0)},
		{app.BottomLeft, "слева внизу", cell(0, 1)},
		{app.BottomCenter, "внизу по центру", cell(1, 1)},
		{app.BottomRight, "справа внизу", cell(2, 1)},
	}
}

// zoneAt is the place under a point of the picture ("" — outside the screen).
func zoneAt(screen rect, x, y int32) string {
	for _, z := range zones(screen) {
		if in(z.box, x, y) {
			return z.position
		}
	}
	return ""
}

// zoneName is the place in words.
func zoneName(position string) string {
	for _, z := range zones(rect{}) {
		if z.position == position {
			return z.name
		}
	}
	return ""
}
