package winui

import (
	"fmt"
	"strings"
	"time"
	u16 "unicode/utf16"

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
	if shown := len(q.Visible) - q.Calls; q.Browsing && shown > 0 {
		return fmt.Sprintf("%d–%d из %d · колесо листает", q.Offset+1, q.Offset+shown, q.Listed)
	}
	// the number of the strip is what is not on the screen; when everything is there, all of them
	t := fmt.Sprintf("%d %s", q.Unread, plural(q.Unread, "уведомление", "уведомления", "уведомлений"))
	if q.Hidden > 0 && q.Hidden < q.Unread {
		t = fmt.Sprintf("Ещё %d %s", q.Hidden, plural(q.Hidden, "уведомление", "уведомления", "уведомлений"))
	}
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

// talkButtons splits the buttons of «Разговор» (contract 1.4 §5.3): first — «Карточка клиента», second — the main
// «Написать в …» (or the next button when there is no messenger), menu — the rest, in their order, behind «▾».
// -1 — no such button.
func talkButtons(c api.Card) (first, second int, menu []int) {
	first, second = -1, -1
	for i, a := range c.Actions {
		if a.ID == "client" && first < 0 {
			first = i
		}
	}
	for i, a := range c.Actions { // the primary message, else the first message
		if a.ID == "message" && i != first && (second < 0 || a.Primary && !c.Actions[second].Primary) {
			second = i
		}
	}
	for i := range c.Actions {
		if i == first || i == second {
			continue
		}
		if first < 0 {
			first = i
			continue
		}
		if second < 0 {
			second = i
			continue
		}
		menu = append(menu, i)
	}
	return first, second, menu
}

// The answer in a card (contract 1.10 §5.8), in logical pixels of a card of the width cardW. Closed: a field «Ответить…»
// under the message. Open: the switch of the modes (more than one), the text, the files, the words of the state, the
// buttons.
const (
	cardW        = 360 // the width of a card
	replyClosedH = 116 // a card with the field «Ответить…»
	replyEditH   = 60  // three lines of text
	replyFileH   = 22
)

type replyRects struct {
	Box                  rect // closed: the field «Ответить…»; open: the frame of the text
	Modes                []rect
	Files                []rect // a row each; its ✕ is fileX
	Status               rect
	Attach, Cancel, Send rect
	H                    int32
}

// replyOpen: the answer of this card is being written.
func replyOpen(c api.Card, d *app.Draft) bool { return c.Reply != nil && d != nil && d.Key == c.Key }

func replyLayout(c api.Card, d *app.Draft) replyRects {
	const left, right = 14, cardW - 14
	var r replyRects
	if !replyOpen(c, d) {
		r.Box, r.H = rect{left, 78, right, 104}, replyClosedH
		return r
	}
	y := int32(76)
	if n := int32(len(c.Reply.Modes)); n > 1 {
		const g = 6
		w := (right - left - g*(n-1)) / n
		for i := range n {
			x := left + i*(w+g)
			r.Modes = append(r.Modes, rect{x, y, x + w, y + 26})
		}
		y += 32
	}
	r.Box = rect{left, y, right, y + replyEditH}
	y += replyEditH + 6
	for range d.Files {
		r.Files = append(r.Files, rect{left, y, right, y + replyFileH})
		y += replyFileH
	}
	if len(d.Files) > 0 {
		y += 4
	}
	if d.Status != "" {
		r.Status = rect{left, y, right, y + 18}
		y += 22
	}
	send := int32(104)
	if c.Reply.Takes {
		send = 150
	}
	r.Send = rect{right - send, y, right, y + 28}
	r.Cancel = rect{r.Send.Left - 6 - 76, y, r.Send.Left - 6, y + 28}
	if c.Reply.Files != nil {
		r.Attach = rect{left, y, left + 84, y + 28}
	}
	r.H = y + 28 + 12
	return r
}

// fileX is the ✕ of a row of a file.
func fileX(row rect) rect { return rect{row.Right - 22, row.Top, row.Right, row.Bottom} }

// replyHint is the field of a closed answer.
func replyHint(c api.Card) string {
	if c.Reply.Takes {
		return "Ответить и взять беседу…"
	}
	if len(c.Reply.Modes) == 1 && c.Reply.Modes[0].Title != "" {
		return c.Reply.Modes[0].Title + "…"
	}
	return "Ответить…"
}

// sendLabel is the main button of an open answer.
func sendLabel(c api.Card, d *app.Draft) string {
	switch {
	case d.Busy:
		return "Отправляю…"
	case c.Reply.Takes:
		return "Взять и отправить"
	}
	return "Отправить"
}

// fileText is a row of a file: the name, the size and what happens to it.
func fileText(f app.File) string {
	s := f.Name + " · " + size(f.Size)
	switch {
	case f.Uploading:
		s += " · загружаю…"
	case f.Error != "":
		s += " · " + f.Error
	}
	return s
}

func size(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d Б", n)
	case n < 1<<20:
		return fmt.Sprintf("%d КБ", (n+1<<9)>>10)
	}
	return strings.Replace(fmt.Sprintf("%.1f МБ", float64(n)/(1<<20)), ".", ",", 1)
}

// chosen reads the answer of the dialog: one full path, or the folder and the names, each ending with a zero.
func chosen(buf []uint16) []string {
	var parts []string
	for start, i := 0, 0; i < len(buf); i++ {
		if buf[i] != 0 {
			continue
		}
		if i == start {
			break
		}
		parts = append(parts, string(u16.Decode(buf[start:i])))
		start = i + 1
	}
	if len(parts) <= 1 {
		return parts
	}
	out := make([]string, 0, len(parts)-1)
	for _, n := range parts[1:] {
		out = append(out, strings.TrimRight(parts[0], `\`)+`\`+n)
	}
	return out
}
