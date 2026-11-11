package winui

import (
	"fmt"
	"strings"
	"testing"
	"time"
	u16 "unicode/utf16"

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
		{queue.View{Unread: 1}, "1 уведомление"},
		{queue.View{Unread: 3}, "3 уведомления"},
		{queue.View{Unread: 5, Hidden: 2}, "Ещё 2 уведомления"},
		{queue.View{Unread: 5, Hidden: 5}, "5 уведомлений"},
		{queue.View{Unread: 11, Hidden: 8, DND: true}, "Ещё 8 уведомлений · не беспокоить"},
		{queue.View{Unread: 12, Browsing: true, Offset: 3, Listed: 12, Visible: make([]api.Card, 3)}, "4–6 из 12 · колесо листает"},
		{queue.View{Unread: 12, Browsing: true, Offset: 3, Listed: 12, Calls: 1, Visible: make([]api.Card, 3)}, "4–5 из 12 · колесо листает"},
		{queue.View{Unread: 12, Browsing: true, Listed: 12, Calls: 3, Visible: make([]api.Card, 3)}, "12 уведомлений"},
	} {
		if got := stripText(c.q); got != c.want {
			t.Errorf("%+v: %q", c.q, got)
		}
	}
}

func TestTalkButtons(t *testing.T) {
	acts := func(ids ...string) api.Card {
		c := api.Card{}
		for _, id := range ids {
			a := api.Action{ID: id, Title: id, URL: "/" + id}
			if strings.HasSuffix(id, "*") {
				a.ID, a.Primary = strings.TrimSuffix(id, "*"), true
			}
			c.Actions = append(c.Actions, a)
		}
		return c
	}
	for _, tc := range []struct {
		card          api.Card
		first, second int
		menu          string
	}{
		{acts("client", "message*", "message", "check"), 0, 1, "[2 3]"},
		{acts("client", "message", "message*", "check"), 0, 2, "[1 3]"},
		{acts("client", "check"), 0, 1, "[]"},
		{acts("message*", "check"), 1, 0, "[]"},
		{acts(), -1, -1, "[]"},
	} {
		f, s, m := talkButtons(tc.card)
		if f != tc.first || s != tc.second || fmt.Sprint(append([]int{}, m...)) != tc.menu {
			t.Errorf("%v: %d %d %v", tc.card.Actions, f, s, m)
		}
	}
}

func TestReplyLayout(t *testing.T) {
	c := api.Card{Key: "chat-1", Reply: &api.Reply{Modes: []api.Mode{{ID: "client"}, {ID: "internal"}}, Files: &api.FileLimits{}}}
	if r := replyLayout(c, nil, 0); r.H != replyClosedH || r.Box.Top < 70 {
		t.Fatalf("closed %+v", r)
	}
	d := &app.Draft{Key: "chat-1", Files: []app.File{{Name: "a"}, {Name: "b"}}, Status: "Отправляю…"}
	r := replyLayout(c, d, 0)
	if len(r.Modes) != 2 || r.Modes[0].Right >= r.Modes[1].Left || len(r.Files) != 2 || r.Status.Bottom == 0 ||
		r.Attach.Right == 0 || r.Cancel.Right > r.Send.Left || r.Send.Right != cardW-14 || r.H != r.Send.Bottom+12 {
		t.Fatalf("open %+v", r)
	}
	for _, x := range append(r.Files, r.Status, r.Box) {
		if x.Top < r.Modes[0].Bottom || x.Bottom > r.Send.Top {
			t.Fatalf("overlap %+v in %+v", x, r)
		}
	}
	one := api.Card{Key: "chat-2", Reply: &api.Reply{Modes: []api.Mode{{ID: "chat", Title: "Ответ Марии"}}, Takes: true}}
	if s := replyLayout(c, nil, stickerShift); s.H != replyClosedH+stickerShift || s.Box.Top != replyLayout(c, nil, 0).Box.Top+stickerShift {
		t.Errorf("with a sticker %+v", s)
	}
	r = replyLayout(one, &app.Draft{Key: "chat-2"}, 0)
	if len(r.Modes) != 0 || r.Attach.Right != 0 || r.Send.Right-r.Send.Left != 150 {
		t.Fatalf("one mode %+v", r)
	}
	if replyHint(one) != "Ответить и взять беседу…" || sendLabel(one, &app.Draft{}) != "Взять и отправить" {
		t.Fatal("takes words")
	}
	if got := size(1536 << 10); got != "1,5 МБ" {
		t.Fatal(got)
	}
	if got := fileText(app.File{Name: "a.pdf", Size: 2048, Uploading: true}); got != "a.pdf · 2 КБ · загружаю…" {
		t.Fatal(got)
	}
}

func TestChosen(t *testing.T) {
	enc := func(s string) []uint16 { return append(u16.Encode([]rune(s)), make([]uint16, 8)...) }
	if got := chosen(enc("C:\\Док\\счёт.pdf\x00")); len(got) != 1 || got[0] != `C:\Док\счёт.pdf` {
		t.Fatalf("%q", got)
	}
	if got := chosen(enc("C:\\\x00a.pdf\x00b.pdf\x00")); len(got) != 2 || got[0] != `C:\a.pdf` || got[1] != `C:\b.pdf` {
		t.Fatalf("%q", got)
	}
	if got := chosen(make([]uint16, 4)); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}
