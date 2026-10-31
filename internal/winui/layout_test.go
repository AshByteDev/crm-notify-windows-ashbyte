package winui

import (
	"testing"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/queue"
)

func TestStack(t *testing.T) {
	area := rect{0, 0, 1000, 800}
	heights := []int32{100, 30}
	for _, c := range []struct {
		position string
		want     []point
	}{
		{app.TopLeft, []point{{10, 10}, {10, 115}}},
		{app.TopCenter, []point{{350, 10}, {350, 115}}},
		{app.TopRight, []point{{690, 10}, {690, 115}}},
		{app.BottomLeft, []point{{10, 690}, {10, 655}}},
		{app.BottomCenter, []point{{350, 690}, {350, 655}}},
		{app.BottomRight, []point{{690, 690}, {690, 655}}},
		{"", []point{{690, 690}, {690, 655}}},
	} {
		got := stack(c.position, area, 300, heights, 10, 5)
		if len(got) != 2 || got[0] != c.want[0] || got[1] != c.want[1] {
			t.Errorf("%q: %v, want %v", c.position, got, c.want)
		}
	}
	// the work area need not start at zero: a taskbar on the left or at the top
	if got := stack(app.TopLeft, rect{60, 40, 1000, 800}, 300, heights, 10, 5); got[0] != (point{70, 50}) {
		t.Errorf("shifted area: %v", got)
	}
}

func TestZones(t *testing.T) {
	screen := rect{100, 50, 400, 250}
	for _, c := range []struct {
		x, y int32
		want string
	}{
		{100, 50, app.TopLeft}, {250, 60, app.TopCenter}, {399, 149, app.TopRight},
		{100, 150, app.BottomLeft}, {250, 200, app.BottomCenter}, {399, 249, app.BottomRight},
		{99, 60, ""}, {400, 60, ""}, {250, 250, ""},
	} {
		if got := zoneAt(screen, c.x, c.y); got != c.want {
			t.Errorf("(%d,%d): %q, want %q", c.x, c.y, got, c.want)
		}
	}
	for _, z := range zones(screen) {
		if zoneName(z.position) == "" {
			t.Errorf("%s has no name", z.position)
		}
	}
}

func TestClock(t *testing.T) {
	at := time.Date(2026, 10, 31, 9, 5, 0, 0, time.Local)
	if got := clock(at.UTC()); got != "09:05" {
		t.Errorf("local time: %q", got)
	}
	if got := clock(time.Time{}); got != "" {
		t.Errorf("no time: %q", got)
	}
}

func TestCardTitle(t *testing.T) {
	if got := cardTitle(api.Card{Title: "Мария", Count: 1}); got != "Мария" {
		t.Error(got)
	}
	if got := cardTitle(api.Card{Title: "Мария", Count: 4}); got != "Мария (4)" {
		t.Error(got)
	}
}

func TestStripText(t *testing.T) {
	for _, c := range []struct {
		q    queue.View
		want string
	}{
		{queue.View{}, ""},
		{queue.View{Unread: 1}, "1 уведомление — открыть AshCRM"},
		{queue.View{Unread: 3}, "3 уведомления — открыть AshCRM"},
		{queue.View{Unread: 11, DND: true}, "11 уведомлений · не беспокоить — открыть AshCRM"},
	} {
		if got := stripText(c.q); got != c.want {
			t.Errorf("%+v: %q", c.q, got)
		}
	}
}
