//go:build windows

package winui

import (
	"strings"
	"syscall"
	u16 "unicode/utf16"
	"unsafe"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
)

// The answer in a card (contract 1.10 §5.8). The text is an EDIT inside the card that shows the answer; the rest is
// drawn. A card does not take the focus (WS_EX_NOACTIVATE) — the one with the answer does while it is open, so the
// keyboard reaches the field. Enter sends, Shift+Enter is a new line, Esc gives up. Files: «Файл» or dropped on the card.

// replyUI is the field of the one answer.
type replyUI struct {
	key  string
	edit uintptr
	host *slot   // the card that shows it now
	proc uintptr // the procedure of EDIT
	cb   uintptr // editProc, made once
}

// wParam of wmReplyKey
const (
	replySend = 1 + iota
	replyCancel
)

// what a click hits in the answer
const (
	hitReplyBox = 50 + iota // the closed field, the frame of the open one
	hitAttach
	hitCancel
	hitSend
	hitInert       // the rest of an open answer: a click there does not open the CRM
	hitMode  = 100 // + the mode
	hitFile  = 200 // + the file: its ✕
)

var (
	colRed    = rgb(0xD9, 0x3B, 0x30)
	colViolet = rgb(0x8E, 0x5C, 0xD9) // a note inside the conversation
	colTake   = rgb(0xFF, 0x6B, 0x00) // «Новая беседа»: the answer takes it
	colField  = rgb(0xF5, 0xF7, 0xFA)
)

// sr scales a rect of logical pixels.
func (u *UI) sr(r rect) rect {
	return rect{u.pi(int(r.Left)), u.pi(int(r.Top)), u.pi(int(r.Right)), u.pi(int(r.Bottom))}
}

func (u *UI) replyHit(s *slot, x, y int32) int {
	if s.kind != "card" || s.card.Reply == nil {
		return hitNone
	}
	d := u.draft
	l := replyLayout(s.card, d, shiftFor(s.pic != nil))
	if !replyOpen(s.card, d) {
		if in(u.sr(l.Box), x, y) {
			return hitReplyBox
		}
		return hitNone
	}
	for i, m := range l.Modes {
		if in(u.sr(m), x, y) {
			return hitMode + i
		}
	}
	for i, f := range l.Files {
		if in(u.sr(fileX(f)), x, y) {
			return hitFile + i
		}
	}
	switch {
	case in(u.sr(l.Attach), x, y):
		return hitAttach
	case in(u.sr(l.Cancel), x, y):
		return hitCancel
	case in(u.sr(l.Send), x, y):
		return hitSend
	case in(u.sr(l.Box), x, y):
		return hitReplyBox
	case y >= u.pi(74):
		return hitInert
	}
	return hitNone
}

// replyClick handles a click in the answer; false — it was not there.
func (u *UI) replyClick(s *slot, h int) bool {
	switch {
	case h == hitReplyBox:
		u.openReply(s)
	case h == hitAttach:
		u.pickFiles(s)
	case h == hitCancel:
		u.A.CancelReply()
	case h == hitSend:
		u.sendReply()
	case h == hitInert:
	case h >= hitFile:
		u.A.RemoveFile(h - hitFile)
	case h >= hitMode:
		if i := h - hitMode; i < len(s.card.Reply.Modes) {
			u.A.SetReplyMode(s.card.Reply.Modes[i].ID)
		}
	default:
		return false
	}
	if u.rep.edit != 0 && u.rep.host == s {
		pSetFocus.Call(u.rep.edit)
	}
	return true
}

// openReply opens the answer of a card; a started answer of another card is given up after a question.
func (u *UI) openReply(s *slot) {
	if d := u.draft; d != nil && d.Key != s.card.Key {
		if (u.replyText() != "" || len(d.Files) > 0) && !confirm("Отменить начатый ответ в другой карточке?") {
			return
		}
		if !u.A.CancelReply() {
			return
		}
	}
	if u.A.StartReply(s.card.Key) {
		u.refresh()
	}
}

func (u *UI) sendReply() {
	if u.rep.edit != 0 {
		u.A.SendReply(u.replyText())
	}
}

// keepReply: the person is asked before an action of the card ends an answer with text.
func (u *UI) keepReply(s *slot) bool {
	return u.rep.edit != 0 && u.rep.key == s.card.Key && strings.TrimSpace(u.replyText()) != "" &&
		!confirm("Отменить начатый ответ?")
}

func (u *UI) replyText() string {
	if u.rep.edit == 0 {
		return ""
	}
	n, _, _ := pGetWindowTextLengthW.Call(u.rep.edit)
	buf := make([]uint16, n+1)
	pGetWindowTextW.Call(u.rep.edit, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

// syncReply puts the field into the card of the answer (after refresh has placed the cards).
func (u *UI) syncReply() {
	d := u.draft
	if d == nil {
		u.closeEdit()
		return
	}
	var host *slot
	for _, s := range u.slots {
		if s.shown && s.kind == "card" && s.card.Key == d.Key && s.card.Reply != nil {
			host = s
		}
	}
	if host == nil {
		if u.rep.edit != 0 {
			pShowWindow.Call(u.rep.edit, swHide)
		}
		return
	}
	if u.rep.edit == 0 || u.rep.key != d.Key {
		u.closeEdit()
		if u.rep.cb == 0 {
			u.rep.cb = syscall.NewCallback(u.editProc)
		}
		e, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16("EDIT"))), 0, wsChild|esMultiline|esAutoVScroll,
			0, 0, 0, 0, host.hwnd, 0, u.inst, 0)
		if e == 0 {
			return
		}
		pSendMessageW.Call(e, emSetLimitText, app.MaxReplyText, 0)
		u.rep.proc, _, _ = pSetWindowLongPtrW.Call(e, gwlpWndProc, u.rep.cb)
		u.rep.edit, u.rep.key, u.rep.host = e, d.Key, host
		u.takeFocus(host, true)
		defer func() { pSetForegroundWindow.Call(host.hwnd); pSetFocus.Call(e) }()
	} else if u.rep.host != host { // the card moved to another window of the stack
		focused, _, _ := pGetFocus.Call()
		u.takeFocus(u.rep.host, false)
		pSetParent.Call(u.rep.edit, host.hwnd)
		u.rep.host = host
		u.takeFocus(host, true)
		if focused == u.rep.edit {
			pSetForegroundWindow.Call(host.hwnd)
			pSetFocus.Call(u.rep.edit)
		}
	}
	box := u.sr(replyLayout(host.card, d, shiftFor(host.pic != nil)).Box)
	in := rect{box.Left + u.pi(8), box.Top + u.pi(6), box.Right - u.pi(8), box.Bottom - u.pi(6)}
	pSendMessageW.Call(u.rep.edit, wmSetFont, u.font(13, false), 0)
	pSetWindowPos.Call(u.rep.edit, 0, uintptr(in.Left), uintptr(in.Top), uintptr(in.Right-in.Left), uintptr(in.Bottom-in.Top),
		swpNoActivate|swpNoZOrder|swpShowWindow)
	ro := uintptr(0)
	if d.Sending {
		ro = 1
	}
	pSendMessageW.Call(u.rep.edit, emSetReadOnly, ro, 0)
}

// takeFocus lets a card take the keyboard and the dropped files (an open answer) or gives them back.
func (u *UI) takeFocus(s *slot, on bool) {
	if s == nil {
		return
	}
	ex, _, _ := pGetWindowLongPtrW.Call(s.hwnd, gwlExStyle)
	drop := uintptr(0)
	if on {
		ex &^= wsExNoActivate
		drop = 1
	} else {
		ex |= wsExNoActivate
	}
	pSetWindowLongPtrW.Call(s.hwnd, gwlExStyle, ex)
	pDragAcceptFiles.Call(s.hwnd, drop)
}

func (u *UI) closeEdit() {
	if u.rep.edit == 0 {
		return
	}
	pDestroyWindow.Call(u.rep.edit)
	u.takeFocus(u.rep.host, false)
	u.rep.edit, u.rep.key, u.rep.host, u.rep.proc = 0, "", nil, 0
}

// editProc: Enter sends, Shift+Enter is a new line, Esc gives up, Ctrl+A selects all. The actions go through the tray
// window: the field must not be destroyed inside its own procedure.
func (u *UI) editProc(hwnd, m, wp, lp uintptr) uintptr {
	shift := func() bool { r, _, _ := pGetKeyState.Call(vkShift); return int16(r) < 0 }
	switch uint32(m) {
	case wmKeyDown:
		if wp == vkReturn && !shift() {
			pPostMessageW.Call(u.tray, wmReplyKey, replySend, 0)
			return 0
		}
		if wp == vkEscape {
			pPostMessageW.Call(u.tray, wmReplyKey, replyCancel, 0)
			return 0
		}
	case wmChar:
		if wp == '\r' && !shift() || wp == vkEscape {
			return 0
		}
		if wp == 1 { // Ctrl+A
			pSendMessageW.Call(hwnd, emSetSel, 0, ^uintptr(0))
			return 0
		}
	}
	r, _, _ := pCallWindowProcW.Call(u.rep.proc, hwnd, m, wp, lp)
	return r
}

// replyKey is Enter or Esc of the field.
func (u *UI) replyKey(what uintptr) {
	switch what {
	case replySend:
		u.sendReply()
	case replyCancel:
		u.A.CancelReply()
	}
}

// dropped attaches the files dropped on the card of the answer.
func (u *UI) dropped(s *slot, hdrop uintptr) {
	defer pDragFinish.Call(hdrop)
	if s != u.rep.host {
		return
	}
	n, _, _ := pDragQueryFileW.Call(hdrop, 0xFFFFFFFF, 0, 0)
	for i := uintptr(0); i < n; i++ {
		l, _, _ := pDragQueryFileW.Call(hdrop, i, 0, 0)
		buf := make([]uint16, l+1)
		pDragQueryFileW.Call(hdrop, i, uintptr(unsafe.Pointer(&buf[0])), l+1)
		u.A.Attach(syscall.UTF16ToString(buf))
	}
}

// pickFiles is «Файл»: the dialog of Windows, several files at once.
func (u *UI) pickFiles(s *slot) {
	buf := make([]uint16, 32<<10)
	filter := append(u16.Encode([]rune("Все файлы")), 0, '*', '.', '*', 0, 0)
	o := openFileName{Owner: s.hwnd, Filter: &filter[0], File: &buf[0], MaxFile: uint32(len(buf)),
		Title: utf16("Приложить файлы к ответу"), Flags: ofnExplorer | ofnFileMust | ofnPathMust | ofnMulti | ofnNoChangeDir}
	o.StructSize = uint32(unsafe.Sizeof(o))
	if r, _, _ := pGetOpenFileNameW.Call(ptr(&o)); r == 0 {
		return
	}
	for _, p := range chosen(buf) {
		u.A.Attach(p)
	}
}

// badges are the marks before the title: «Новая беседа» (the answer takes it) and «внутри» (a note of a colleague).
func (u *UI) badges(p painter, c api.Card, left int32) int32 {
	type badge struct {
		text  string
		color uintptr
	}
	var list []badge
	if c.Reply != nil && c.Reply.Takes {
		list = append(list, badge{"Новая беседа", colTake})
	}
	if c.From == api.FromStaff {
		list = append(list, badge{"внутри", colViolet})
	}
	f := u.font(11, true)
	for _, b := range list {
		w := p.measure(b.text, f) + u.pi(12)
		r := rect{left, u.pi(9), left + w, u.pi(27)}
		p.round(r, b.color, b.color, u.pi(9))
		p.text(b.text, r, f, colWhite, dtSingleLine|dtVCenter|dtCenter)
		left += w + u.pi(6)
	}
	return left
}

// drawReply is the field «Ответить…» or the open answer.
func (u *UI) drawReply(p painter, s *slot) {
	d := u.draft
	c := s.card
	l := replyLayout(c, d, shiftFor(s.pic != nil))
	if !replyOpen(c, d) {
		box, border := u.sr(l.Box), colBorder
		if s.hover == hitReplyBox {
			border = colBlue
		}
		p.round(box, colField, border, u.pi(10))
		p.text("↩", rect{box.Left + u.pi(10), box.Top, box.Left + u.pi(28), box.Bottom}, u.symbolFont(12), colMuted, dtSingleLine|dtVCenter)
		p.text(replyHint(c), rect{box.Left + u.pi(30), box.Top, box.Right - u.pi(10), box.Bottom}, u.font(12, false), colMuted,
			dtSingleLine|dtVCenter|dtEndEllipsis)
		return
	}
	for i, m := range l.Modes {
		r := u.sr(m)
		bg, fg := colBtn, colTitle
		if s.hover == hitMode+i {
			bg = colBtnHov
		}
		if c.Reply.Modes[i].ID == d.Mode {
			bg, fg = colNavy, colWhite
		}
		title := c.Reply.Modes[i].Title
		if title == "" {
			title = c.Reply.Modes[i].ID
		}
		p.round(r, bg, bg, u.pi(8))
		p.text(title, rect{r.Left + u.pi(6), r.Top, r.Right - u.pi(6), r.Bottom}, u.font(12, c.Reply.Modes[i].ID == d.Mode), fg,
			dtSingleLine|dtVCenter|dtCenter|dtEndEllipsis)
	}
	border := colBlue
	if d.Sending {
		border = colBorder
	}
	p.round(u.sr(l.Box), colWhite, border, u.pi(10))
	for i, f := range l.Files {
		r, x := u.sr(f), u.sr(fileX(f))
		fg := colBody
		if d.Files[i].Error != "" {
			fg = colRed
		}
		p.text("📎", rect{r.Left, r.Top, r.Left + u.pi(18), r.Bottom}, u.symbolFont(11), colMuted, dtSingleLine|dtVCenter)
		p.text(fileText(d.Files[i]), rect{r.Left + u.pi(20), r.Top, x.Left - u.pi(4), r.Bottom}, u.font(12, false), fg,
			dtSingleLine|dtVCenter|dtEndEllipsis)
		if !d.Sending {
			if s.hover == hitFile+i {
				p.round(x, colBtnHov, colBtnHov, u.pi(6))
			}
			p.text("✕", x, u.font(11, false), colMuted, dtSingleLine|dtVCenter|dtCenter)
		}
	}
	if d.Status != "" {
		fg := colMuted
		if d.Failed {
			fg = colRed
		}
		p.text(d.Status, u.sr(l.Status), u.font(12, false), fg, dtSingleLine|dtVCenter|dtEndEllipsis)
	}
	button := func(r rect, hit int, label string, main, off bool) {
		if r.Right == 0 {
			return
		}
		r = u.sr(r)
		bg, fg := colBtn, colTitle
		switch {
		case main && off:
			bg, fg = colNavyOff, colWhite
		case main && s.hover == hit:
			bg, fg = colBlueHov, colWhite
		case main:
			bg, fg = colBlue, colWhite
		case off:
			fg = colMuted
		case s.hover == hit:
			bg = colBtnHov
		}
		p.round(r, bg, bg, u.pi(8))
		p.text(label, r, u.font(12, main), fg, dtSingleLine|dtVCenter|dtCenter|dtEndEllipsis)
	}
	if l.Attach.Right > 0 {
		button(l.Attach, hitAttach, "", false, d.Sending)
		r := u.sr(l.Attach)
		p.text("📎", rect{r.Left + u.pi(12), r.Top, r.Left + u.pi(30), r.Bottom}, u.symbolFont(12), colTitle, dtSingleLine|dtVCenter)
		p.text("Файл", rect{r.Left + u.pi(32), r.Top, r.Right - u.pi(8), r.Bottom}, u.font(12, false), colTitle, dtSingleLine|dtVCenter)
	}
	button(l.Cancel, hitCancel, "Отмена", false, d.Busy)
	button(l.Send, hitSend, sendLabel(c, d), true, d.Sending)
}

// measure is the width of a line of text.
func (p painter) measure(s string, font uintptr) int32 {
	pSelectObject.Call(p.dc, font)
	r := rect{0, 0, 2000, 200}
	pDrawTextW.Call(p.dc, uintptr(unsafe.Pointer(utf16(s))), ^uintptr(0), ptr(&r), dtCalcRect|dtSingleLine|dtNoPrefix)
	return r.Right
}
