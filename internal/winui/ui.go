//go:build windows

// Package winui is the face of the app on Windows: the tray icon with its menu, up to three cards that stay until an action and
// a strip with the number of unread ones at the edge of the screen (docs/APP_SPEC.md §3.1, §3.4). It draws with plain
// Win32/GDI — no runtime to ship.
package winui

import (
	"context"
	"fmt"
	"image"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/queue"
)

// Logical sizes (scaled by the DPI of the screen).
const (
	cardH    = 76  // the line of the sender and two lines of the message
	serviceH = 116 // the cards with worded buttons
	talkH    = 108 // «Разговор»: the title, the number, a row of worded buttons (contract 1.4)
	moreH    = 32
	dot      = 20 // the place of a coloured circle of a card, what a click hits
	dotDraw  = 18 // the circle drawn in it (TZ: at least 16)
	headH    = 62 // the band with the sign at the top of the windows
	dotGap   = 8  // between them (TZ: at least 6)
	gap      = 8
	margin   = 12
	radius   = 14
	slotMax  = queue.Max + 1 // three cards and the strip of the unread
)

const trayClassName = "AshCRMNotifyTray"

// timerHide wakes the UI when the next card must leave the screen.
const timerHide = 1

// timerAsk ends the question of «Прочитать все» in the strip.
const timerAsk = 2

// askFor is how long the strip asks «Прочитать все N?» before it gives up.
const askFor = 5 * time.Second

// menu commands
const (
	cmdLogin = iota + 1
	cmdLogout
	cmdOpenCRM
	cmdDND
	cmdAutostart
	cmdUpdates
	cmdQuit
	cmdPortal
	cmdCheckNow
	cmdUpdateNow
	cmdCancelLogin
	cmdSettings
	cmdReadAll
	cmdHideAll
)

// «Прочитать все» asks first when it reads more than this.
const confirmOver = 5

// what a click hits on a card
const (
	hitNone = iota
	hitBody
	hitOpen
	hitRead
	hitClose
	hitLogin
	hitHideAll // the words of the strip
	hitReadAll
	hitAct1 // the buttons of «Разговор»: «Карточка клиента», «Написать в …», «▾»
	hitAct2
	hitActMenu
)

// cmdAction + i in the menu «▾» of «Разговор» is the action i of the card.
const cmdAction = 1000

// slot is one window of the stack: a card, the «sign in» card or the strip of the unread.
type slot struct {
	hwnd  uintptr
	card  api.Card
	kind  string // card | signin | more
	text  string // the strip / the error of the sign-in card
	hover int
	shown bool
	pic   image.Image // the sticker of the card (contract 1.11); nil — the text
}

// UI owns every window; all of it runs on one locked OS thread (the Win32 rule).
type UI struct {
	A   *app.App
	Ctx context.Context

	inst      uintptr
	tray      uintptr
	trayReady atomic.Uintptr // the tray window for Notify (set once it exists)
	trayMsg   uint32         // TaskbarCreated: Explorer restarted, the icon must be added again
	quitAsked atomic.Bool
	scale     float64
	icons     map[string]uintptr
	iconKey   string
	slots     [slotMax]*slot
	byHwnd    map[uintptr]*slot
	fonts     map[string]uintptr
	dots      map[string][]byte // the pixels of the circles by colour, sign, hover and size
	pics      map[any][]byte    // the pixels of the stickers by picture and size
	signinOK  bool              // the person closed the «sign in» card

	portal       portal
	portalAsked  bool
	portalResult atomic.Pointer[error]

	settings settings

	rep   replyUI    // the field of the answer
	draft *app.Draft // the answer being written (app.View.Reply of the last refresh)
	// asking: the strip asks whether to read all (the number); 0 — it does not. In the strip, not in a window: a
	// window of Windows opens behind the others, the cards do not take the focus.
	asking int
}

// AlreadyRunning holds a named mutex for the session of the user: a second start just exits.
func AlreadyRunning() bool {
	h, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(utf16(`Local\AshCRMNotify`))))
	if err == syscall.Errno(errAlreadyExis) {
		pCloseHandle.Call(h) // our handle must not keep the mutex of the other copy alive
		return true
	}
	return false
}

// CloseRunning asks the running copy of this user to close: windows are seen only within their session.
func CloseRunning() {
	if h, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(utf16(trayClassName))), 0); h != 0 {
		pPostMessageW.Call(h, wmClose, 0, 0)
	}
}

// Quit closes the app (from any goroutine): the tray window goes, the message loop ends.
func (u *UI) Quit() {
	u.quitAsked.Store(true) // the tray window may not exist yet: Run looks at this
	if h := u.trayReady.Load(); h != 0 {
		pPostMessageW.Call(h, wmClose, 0, 0)
	}
}

// confirm asks yes/no.
func confirm(text string) bool {
	// YESNO|QUESTION|SETFOREGROUND|TOPMOST: over the other windows, not somewhere behind them
	r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16("AshCRM Notify"))), 0x4|0x20|0x10000|0x40000)
	return r == 6 // IDYES
}

// Message shows a message box (errors before the tray exists).
func Message(text string) {
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16("AshCRM Notify"))), 0x40)
}

// Notify asks the UI thread to redraw (app.App.Changed; safe from any goroutine, before the windows exist too).
func (u *UI) Notify() {
	if h := u.trayReady.Load(); h != 0 {
		pPostMessageW.Call(h, wmRefresh, 0, 0)
	}
}

// Run creates the tray and the cards and runs the message loop until «Выход».
func (u *UI) Run() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// sharp text on scaled screens; the calls are missing on old Windows 10 builds — then the old way
	if pSetProcessDpiAwarenessContext.Find() == nil {
		pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	} else {
		pSetProcessDPIAware.Call()
	}
	u.scale = 1
	if pGetDpiForSystem.Find() == nil {
		if dpi, _, _ := pGetDpiForSystem.Call(); dpi != 0 {
			u.scale = float64(dpi) / 96
		}
	}
	u.inst, _, _ = pGetModuleHandleW.Call(0)
	u.icons, u.fonts, u.byHwnd = map[string]uintptr{}, map[string]uintptr{}, map[uintptr]*slot{}

	hand, _, _ := pLoadCursorW.Call(0, idcHand)
	arrow, _, _ := pLoadCursorW.Call(0, idcArrow)
	trayClass := wndClassEx{Style: 0, WndProc: syscall.NewCallback(u.trayProc), Instance: u.inst, Cursor: arrow,
		ClassName: utf16(trayClassName)}
	trayClass.Size = uint32(unsafe.Sizeof(trayClass))
	if r, _, err := pRegisterClassExW.Call(ptr(&trayClass)); r == 0 {
		return fmt.Errorf("register tray class: %w", err)
	}
	cardClass := wndClassEx{Style: csDropShadow, WndProc: syscall.NewCallback(u.cardProc), Instance: u.inst, Cursor: hand,
		ClassName: utf16("AshCRMNotifyCard")}
	cardClass.Size = uint32(unsafe.Sizeof(cardClass))
	if r, _, err := pRegisterClassExW.Call(ptr(&cardClass)); r == 0 {
		return fmt.Errorf("register card class: %w", err)
	}
	if err := u.registerPortal(); err != nil {
		return fmt.Errorf("register portal class: %w", err)
	}
	if err := u.registerSettings(); err != nil {
		return fmt.Errorf("register settings class: %w", err)
	}
	u.tray, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(trayClass.ClassName)), 0, 0, 0, 0, 0, 0, 0, 0, u.inst, 0)
	if u.tray == 0 {
		return fmt.Errorf("create tray window")
	}
	tm, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16("TaskbarCreated"))))
	u.trayMsg = uint32(tm)
	for i := range u.slots {
		h, _, _ := pCreateWindowExW.Call(wsExTopmost|wsExToolWindow|wsExNoActivate, uintptr(unsafe.Pointer(cardClass.ClassName)),
			0, wsPopup|wsClipChildren, 0, 0, u.px(cardW), u.px(cardH), 0, 0, u.inst, 0)
		s := &slot{hwnd: h}
		u.slots[i] = s
		u.byHwnd[h] = s
	}
	u.addTray()
	if pWTSRegisterSessionNotification.Find() == nil {
		pWTSRegisterSessionNotification.Call(u.tray, 0) // NOTIFY_FOR_THIS_SESSION
		defer pWTSUnRegisterSessionNotification.Call(u.tray)
	}
	u.trayReady.Store(u.tray)
	if u.quitAsked.Load() {
		pPostMessageW.Call(u.tray, wmClose, 0, 0)
	}
	u.refresh()

	var m msg
	for {
		r, _, _ := pGetMessageW.Call(ptr(&m), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		if u.portal.hwnd != 0 {
			if done, _, _ := pIsDialogMessageW.Call(u.portal.hwnd, ptr(&m)); done != 0 {
				continue // Tab and Enter in the window of the address
			}
		}
		pTranslateMessage.Call(ptr(&m))
		pDispatchMessageW.Call(ptr(&m))
	}
	u.removeTray()
	return nil
}

func (u *UI) px(v int) uintptr { return uintptr(float64(v)*u.scale + 0.5) }
func (u *UI) pi(v int) int32   { return int32(float64(v)*u.scale + 0.5) }

// syncScale follows the DPI of the screen of the cards (GetDpiForSystem stays what it was at the start); it reports a
// change.
func (u *UI) syncScale() bool {
	if pGetDpiForWindow.Find() != nil {
		return false
	}
	dpi, _, _ := pGetDpiForWindow.Call(u.slots[0].hwnd)
	if dpi == 0 || float64(dpi)/96 == u.scale {
		return false
	}
	u.scale = float64(dpi) / 96
	return true
}

// ---- tray ----

func (u *UI) iconFor(v app.View) (string, uintptr) {
	o := LogoOptions{Large: true, Offline: v.State != app.Online, DND: v.State == app.Online && v.DND, Unread: v.Queue.Unread > 0}
	key := fmt.Sprint(o.Offline, o.DND, o.Unread)
	size := max(int(16*u.scale+0.5), 16)
	return key, u.iconOf(fmt.Sprint(key, size), size, o)
}

// iconOf is an icon of the sign of AshCRM, made once per look and size.
func (u *UI) iconOf(cached string, size int, o LogoOptions) uintptr {
	if h, ok := u.icons[cached]; ok {
		return h
	}
	png := LogoPNG(size, o)
	h, _, _ := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000,
		uintptr(size), uintptr(size), 0)
	u.icons[cached] = h
	return h
}

// windowIcons are the big and the small icon of the windows of the app (the caption, Alt+Tab, the taskbar).
func (u *UI) windowIcons() (big, small uintptr) {
	b, s := max(int(32*u.scale+0.5), 32), max(int(16*u.scale+0.5), 16)
	return u.iconOf(fmt.Sprint("window", b), b, LogoOptions{}), u.iconOf(fmt.Sprint("window", s), s, LogoOptions{Large: true})
}

func tipOf(v app.View) string {
	s := "AshCRM — "
	switch v.State {
	case app.Online:
		s += "подключено"
		if v.DND {
			s += ", не беспокоить"
		}
	case app.Connecting:
		s += "подключение…"
	case app.Offline:
		s += "нет связи, переподключаюсь"
	case app.SigningIn:
		s += "вход в браузере…"
	case app.NeedServer:
		s += "укажите адрес портала"
	default:
		s += "войдите в приложение"
	}
	if n := v.Queue.Unread; n > 0 {
		s += fmt.Sprintf(" · уведомлений: %d", n)
	}
	return s
}

func (u *UI) trayData(v app.View) notifyIconData {
	key, icon := u.iconFor(v)
	u.iconKey = key
	d := notifyIconData{Wnd: u.tray, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTray, Icon: icon}
	d.Size = uint32(unsafe.Sizeof(d))
	tip, _ := syscall.UTF16FromString(tipOf(v))
	copy(d.Tip[:len(d.Tip)-1], tip)
	return d
}

func (u *UI) addTray() {
	d := u.trayData(u.A.View())
	pShellNotifyIconW.Call(nimAdd, ptr(&d))
}

func (u *UI) removeTray() {
	d := notifyIconData{Wnd: u.tray, ID: 1}
	d.Size = uint32(unsafe.Sizeof(d))
	pShellNotifyIconW.Call(nimDelete, ptr(&d))
}

func (u *UI) trayProc(hwnd, m, wp, lp uintptr) uintptr {
	switch uint32(m) {
	case wmTray:
		switch uint32(lp) {
		case wmLButtonUp:
			v := u.A.View()
			if v.State == app.NeedServer {
				u.showPortal(v.DefaultServer)
			} else if v.State == app.SignedOut {
				u.A.Login(u.Ctx)
			} else {
				u.signinOK = false
				u.A.OpenCRM()
			}
		case wmRButtonUp:
			u.menu()
		}
		return 0
	case wmRefresh:
		u.refresh()
		return 0
	case wmPortalDone:
		u.portalDone()
		return 0
	case wmReplyKey:
		u.replyKey(wp)
		return 0
	case wmTimer:
		if wp == timerHide {
			pKillTimer.Call(u.tray, timerHide)
			u.A.Q.Tick(time.Now())
			u.refresh()
		}
		if wp == timerAsk {
			u.stopAsking()
		}
		return 0
	case wmPowerBroadcast:
		if wp == pbtResumeAutomatic {
			u.A.Reconnect()
		}
		return 1
	case wmSessionChange:
		switch wp {
		case wtsConsoleDisconnect, wtsRemoteDisconnect:
			u.A.Pause()
		case wtsConsoleConnect, wtsRemoteConnect:
			u.A.Resume(u.Ctx)
		}
		return 0
	case wmDisplayChange, wmSettingChange, wmDpiChanged:
		if u.trayReady.Load() != 0 {
			u.refresh() // the size of the screen, the taskbar or the scale changed
		}
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	if u.trayMsg != 0 && uint32(m) == u.trayMsg {
		u.addTray()
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func (u *UI) menu() {
	v := u.A.View()
	h, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(h)
	add := func(id int, text string, flags uintptr) {
		pAppendMenuW.Call(h, flags, uintptr(id), uintptr(unsafe.Pointer(utf16(text))))
	}
	sep := func() { pAppendMenuW.Call(h, mfSeparator, 0, 0) }
	check := func(on bool) uintptr {
		if on {
			return mfChecked
		}
		return 0
	}
	status := tipOf(v)
	if v.User != "" {
		status = v.User + " · " + strings.TrimPrefix(status, "AshCRM — ")
	}
	add(0, status, mfGrayed)
	if v.Error != "" {
		add(0, v.Error, mfGrayed)
	}
	sep()
	switch v.State {
	case app.NeedServer:
		add(cmdPortal, "Указать адрес портала…", mfString)
	case app.SignedOut:
		add(cmdLogin, "Войти…", mfString)
	case app.SigningIn:
		add(cmdCancelLogin, "Отменить вход", mfString)
	default:
		add(cmdOpenCRM, "Открыть AshCRM", mfString)
	}
	add(cmdDND, "Не беспокоить", check(v.DND))
	sep()
	if v.Server != "" {
		add(0, "Портал: "+strings.TrimPrefix(strings.TrimPrefix(v.Server, "https://"), "http://"), mfGrayed)
		if !v.FixedServer {
			add(cmdPortal, "Сменить портал…", mfString)
		}
	}
	if v.State != app.SignedOut && v.State != app.SigningIn && v.State != app.NeedServer {
		add(cmdLogout, "Выйти из аккаунта", mfString)
	}
	sep()
	add(cmdSettings, "Настройки…", mfString)
	add(cmdAutostart, "Запускать вместе с Windows", check(v.Autostart))
	if !v.Shared {
		add(cmdUpdates, "Проверять обновления автоматически", check(v.CheckUpdates))
	}
	switch {
	case v.Shared: // the administrator decides
	case v.Updating:
		add(0, "Обновляю…", mfGrayed)
	case v.Update != nil:
		add(cmdUpdateNow, "Обновить до версии "+v.Update.Version, mfString)
	default:
		add(cmdCheckNow, "Проверить обновления", mfString)
	}
	sep()
	add(0, "Версия "+v.Version, mfGrayed)
	add(cmdQuit, "Закрыть приложение", mfString)

	var p point
	pGetCursorPos.Call(ptr(&p))
	pSetForegroundWindow.Call(u.tray) // else the menu does not close on a click outside
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmRightButton|tpmReturnCmd|tpmBottomAlign, uintptr(p.X), uintptr(p.Y), 0, u.tray, 0)
	pPostMessageW.Call(u.tray, wmNull, 0, 0)
	switch int(cmd) {
	case cmdLogin:
		u.A.Login(u.Ctx)
	case cmdCancelLogin:
		u.A.CancelLogin()
	case cmdSettings:
		u.showSettings()
	case cmdLogout:
		go u.A.Logout(u.Ctx)
	case cmdOpenCRM:
		u.A.OpenCRM()
	case cmdDND:
		u.A.SetDND(!v.DND)
	case cmdAutostart:
		u.A.SetAutostart(!v.Autostart)
	case cmdUpdates:
		u.A.SetCheckUpdates(!v.CheckUpdates)
	case cmdQuit:
		pDestroyWindow.Call(u.tray)
	case cmdPortal:
		if v.State == app.NeedServer {
			u.showPortal(v.DefaultServer)
		} else if confirm("Сменить портал? Приложение выйдет из аккаунта на " + v.Server + ".") {
			go u.A.ChangeServer(u.Ctx)
		}
	case cmdCheckNow:
		go func() {
			r, err := u.A.CheckUpdate(u.Ctx)
			switch {
			case err != nil:
				Message("Не удалось проверить обновления: " + err.Error())
			case r == nil:
				Message("Установлена последняя версия " + v.Version + ".")
			}
		}()
	case cmdUpdateNow:
		go func() { _ = u.A.Update(u.Ctx) }()
	}
}

// ---- the stack of cards ----

// refresh redraws the tray and puts the windows in their places: the strip at the edge of the screen, then the cards,
// the newest closest to it.
func (u *UI) refresh() {
	rescaled := u.syncScale()
	v := u.A.View()
	u.draft = v.Reply
	if u.settings.hwnd != 0 {
		pInvalidateRect.Call(u.settings.hwnd, 0, 0) // the result of the check
	}
	d := u.trayData(v)
	pShellNotifyIconW.Call(nimModify, ptr(&d))
	type want struct {
		kind, text string
		card       api.Card
		pic        image.Image
	}
	var list []want
	if t := stripText(v.Queue); t != "" {
		if u.asking != 0 {
			t = fmt.Sprintf("Прочитать все %d?", u.asking)
		}
		list = append(list, want{kind: "more", text: t})
	} else {
		u.asking = 0
	}
	if v.State == app.NeedServer {
		if !u.portalAsked {
			u.portalAsked = true
			u.showPortal(v.DefaultServer)
		}
	} else {
		u.portalAsked = false
	}
	if v.State == app.SignedOut && !u.signinOK {
		list = append(list, want{kind: "signin", text: v.Error})
	} else if v.State != app.SignedOut {
		u.signinOK = false
	}
	for _, c := range v.Queue.Visible {
		list = append(list, want{kind: "card", card: c, pic: u.A.Sticker(c)})
	}

	var wa rect
	pSystemParametersInfoW.Call(spiGetWorkArea, 0, ptr(&wa), 0)
	heights := make([]int32, len(list))
	for i, w := range list {
		switch {
		case w.kind == "more":
			heights[i] = u.pi(moreH)
		case w.kind == "signin" || w.card.Key == app.UpdateKey:
			heights[i] = u.pi(serviceH)
		case w.card.Talking():
			heights[i] = u.pi(talkH)
		case w.card.Reply != nil:
			heights[i] = u.pi(int(replyLayout(w.card, v.Reply, shiftFor(w.pic != nil)).H))
		default:
			heights[i] = u.pi(cardH + int(shiftFor(w.pic != nil)))
		}
	}
	places := stack(v.Position, wa, u.pi(cardW), heights, u.pi(margin), u.pi(gap))
	for i, s := range u.slots {
		if i >= len(list) {
			if s.shown {
				pShowWindow.Call(s.hwnd, swHide)
				s.shown = false
			}
			continue
		}
		w, h, at := list[i], heights[i], places[i]
		changed := rescaled || w.card.Reply != nil || s.kind != w.kind || s.text != w.text || s.pic != w.pic || !api.Same(s.card, w.card)
		s.kind, s.text, s.card, s.pic = w.kind, w.text, w.card, w.pic
		pSetWindowPos.Call(s.hwnd, hwndTopmost, uintptr(at.X), uintptr(at.Y), uintptr(u.pi(cardW)), uintptr(h), swpNoActivate|swpShowWindow)
		rgn, _, _ := pCreateRoundRectRgn.Call(0, 0, uintptr(u.pi(cardW)+1), uintptr(h+1), uintptr(u.pi(radius)), uintptr(u.pi(radius)))
		pSetWindowRgn.Call(s.hwnd, rgn, 1)
		if !s.shown {
			pShowWindow.Call(s.hwnd, swShowNoActive)
			s.shown = true
		}
		if changed {
			pInvalidateRect.Call(s.hwnd, 0, 0)
		}
	}
	u.syncReply()
	u.armTimer(v.Queue.Next)
}

// armTimer wakes the UI when the next card must go (a zero time — no card is timed).
func (u *UI) armTimer(at time.Time) {
	if at.IsZero() {
		pKillTimer.Call(u.tray, timerHide)
		return
	}
	ms := max(time.Until(at).Milliseconds()+1, 1)
	pSetTimer.Call(u.tray, timerHide, uintptr(ms), 0)
}

// service: a card of the app that needs a decision: it keeps its worded buttons and its size.
func service(s *slot) bool { return s.kind == "signin" || s.card.Key == app.UpdateKey }

// geometry of a card (client coordinates)
func (u *UI) buttons(s *slot) (open, read, close rect) {
	w := u.pi(cardW)
	if !service(s) { // the circles of the first line, from the text: green «Прочитано», blue «Открыть в CRM», red «Закрыть»
		size, top, step := u.pi(dot), u.pi(8), u.pi(dot+dotGap)
		close = rect{w - u.pi(10) - size, top, w - u.pi(10), top + size}
		next := close
		if s.card.URL != "" { // nowhere to open: no blue circle
			open = rect{next.Left - step, top, next.Right - step, top + size}
			next = open
		}
		if s.card.Kind != queue.KindCall { // a call needs no «Прочитано» (contract §5.4)
			read = rect{next.Left - step, top, next.Right - step, top + size}
		}
		return
	}
	h := u.pi(serviceH)
	bh, bw := u.pi(26), u.pi(96)
	read = rect{w - u.pi(14) - bw, h - u.pi(12) - bh, w - u.pi(14), h - u.pi(12)}
	open = rect{read.Left - u.pi(8) - bw, read.Top, read.Left - u.pi(8), read.Bottom}
	close = rect{w - u.pi(30), u.pi(8), w - u.pi(8), u.pi(30)}
	if s.kind == "signin" {
		read.Left, open = read.Right-u.pi(120), rect{}
	}
	return
}

// talkRects are the worded buttons of «Разговор» in the last row: the first, the second, «▾» (an empty rect — none).
func (u *UI) talkRects(s *slot) (b1, b2, bm rect) {
	first, second, menu := talkButtons(s.card)
	w, h := u.pi(cardW), u.pi(talkH)
	top, bottom := h-u.pi(12)-u.pi(28), h-u.pi(12)
	left, right := u.pi(14), w-u.pi(12)
	if len(menu) > 0 {
		bm = rect{right - u.pi(28), top, right, bottom}
		right = bm.Left - u.pi(6)
	}
	switch {
	case first >= 0 && second >= 0:
		b1 = rect{left, top, left + u.pi(124), bottom}
		b2 = rect{b1.Right + u.pi(6), top, right, bottom}
	case first >= 0:
		b1 = rect{left, top, right, bottom}
	}
	return
}

func (u *UI) hit(s *slot, x, y int32) int {
	if s.kind == "more" {
		hide, read := u.stripButtons(s)
		switch {
		case in(hide, x, y):
			return hitHideAll
		case in(read, x, y):
			return hitReadAll
		}
		return hitBody
	}
	open, read, cl := u.buttons(s)
	if h := u.replyHit(s, x, y); h != hitNone {
		return h
	}
	if s.kind == "card" && s.card.Talking() {
		b1, b2, bm := u.talkRects(s)
		switch {
		case in(b1, x, y):
			return hitAct1
		case in(b2, x, y):
			return hitAct2
		case in(bm, x, y):
			return hitActMenu
		}
	}
	switch {
	case in(cl, x, y):
		return hitClose
	case in(read, x, y) && s.kind == "signin":
		return hitLogin
	case in(read, x, y):
		return hitRead
	case in(open, x, y):
		return hitOpen
	}
	if s.kind == "signin" {
		return hitLogin
	}
	return hitBody
}

// holdKey is what keeps its time under the mouse.
func holdKey(s *slot) string {
	switch s.kind {
	case "card":
		return s.card.Key
	case "more":
		return queue.StripKey
	}
	return ""
}

// stripButtons are the words «Скрыть все» and «Прочитать все» at the right of the strip.
func (u *UI) stripButtons(s *slot) (hide, read rect) {
	w, h := u.pi(cardW), u.pi(moreH)
	read = rect{w - u.pi(10) - u.pi(stripReadW), 0, w - u.pi(10), h}
	hide = rect{read.Left - u.pi(6) - u.pi(stripHideW), 0, read.Left - u.pi(6), h}
	return
}

// the widths of the words of the strip
const (
	stripHideW = 78
	stripReadW = 98
)

// readAll is «Прочитать все»: when it is more than a few, the strip asks first («Да, прочитать» / «Отмена»), the
// second click reads.
func (u *UI) readAll() {
	n := u.A.View().Queue.Unread
	if n > confirmOver && u.asking == 0 {
		u.asking = n
		pSetTimer.Call(u.tray, timerAsk, uintptr(askFor.Milliseconds()), 0)
		u.refresh()
		return
	}
	u.stopAsking()
	u.A.ReadAll(u.Ctx)
}

// stopAsking: the strip shows its number and its words again.
func (u *UI) stopAsking() {
	pKillTimer.Call(u.tray, timerAsk)
	if u.asking != 0 {
		u.asking = 0
		u.refresh()
	}
}

// stripMenu is the right click on the strip.
// actionMenu is «▾» of «Разговор»: the other messengers and the rest of the buttons.
func (u *UI) actionMenu(s *slot) {
	_, _, menu := talkButtons(s.card)
	if len(menu) == 0 {
		return
	}
	h, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(h)
	for _, i := range menu {
		pAppendMenuW.Call(h, mfString, uintptr(cmdAction+i), uintptr(unsafe.Pointer(utf16(s.card.Actions[i].Title))))
	}
	var p point
	pGetCursorPos.Call(ptr(&p))
	pSetForegroundWindow.Call(u.tray) // else the menu does not close on a click outside
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmRightButton|tpmReturnCmd, uintptr(p.X), uintptr(p.Y), 0, u.tray, 0)
	pPostMessageW.Call(u.tray, wmNull, 0, 0)
	if i := int(cmd) - cmdAction; cmd != 0 && i >= 0 {
		u.A.OpenAction(s.card.Key, i)
	}
}

func (u *UI) stripMenu() {
	n := u.A.View().Queue.Unread
	h, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(h)
	pAppendMenuW.Call(h, mfString, cmdHideAll, uintptr(unsafe.Pointer(utf16("Скрыть все"))))
	pAppendMenuW.Call(h, mfString, cmdReadAll, uintptr(unsafe.Pointer(utf16(fmt.Sprintf("Прочитать все (%d)", n)))))
	pAppendMenuW.Call(h, mfSeparator, 0, 0)
	pAppendMenuW.Call(h, mfString, cmdOpenCRM, uintptr(unsafe.Pointer(utf16("Открыть AshCRM"))))
	var p point
	pGetCursorPos.Call(ptr(&p))
	pSetForegroundWindow.Call(u.tray) // else the menu does not close on a click outside
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmRightButton|tpmReturnCmd, uintptr(p.X), uintptr(p.Y), 0, u.tray, 0)
	pPostMessageW.Call(u.tray, wmNull, 0, 0)
	switch int(cmd) {
	case cmdOpenCRM:
		u.A.OpenCRM()
	case cmdHideAll:
		u.A.HideAll()
	case cmdReadAll:
		u.readAll()
	}
}

func (u *UI) cardProc(hwnd, m, wp, lp uintptr) uintptr {
	s := u.byHwnd[hwnd]
	switch uint32(m) {
	case wmPaint:
		if s != nil {
			u.paint(s)
			return 0
		}
	case wmMouseMove:
		if s != nil {
			h := u.hit(s, loword(lp), hiword(lp))
			if h != s.hover {
				s.hover = h
				pInvalidateRect.Call(hwnd, 0, 0)
			}
			t := trackMouseEvent{Flags: tmeLeave, Track: hwnd}
			t.Size = uint32(unsafe.Sizeof(t))
			pTrackMouseEvent.Call(ptr(&t))
			if k := holdKey(s); k != "" && u.A.Q.Hold(k, true) {
				u.refresh()
			}
		}
		return 0
	case wmMouseLeave:
		if s != nil {
			if k := holdKey(s); k != "" && u.A.Q.Hold(k, false) {
				u.refresh()
			}
		}
		if s != nil && s.hover != hitNone {
			s.hover = hitNone
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmLButtonUp:
		if s == nil {
			return 0
		}
		h := u.hit(s, loword(lp), hiword(lp))
		if u.replyClick(s, h) {
			return 0
		}
		if (h == hitClose || h == hitRead) && s.kind == "card" && u.keepReply(s) {
			return 0
		}
		switch h {
		case hitClose:
			if s.kind == "signin" {
				u.signinOK = true
				u.refresh()
			} else if s.kind == "card" {
				u.A.Close(s.card.Key)
			}
		case hitRead:
			if s.card.Key == app.UpdateKey {
				go func() { _ = u.A.Update(u.Ctx) }()
			} else {
				u.A.Read(u.Ctx, s.card.Key)
			}
		case hitOpen:
			if s.card.Key == app.UpdateKey {
				u.A.Close(s.card.Key) // «Позже»
			} else if s.kind == "card" {
				u.A.Open(s.card.Key)
			}
		case hitBody:
			if s.kind == "more" {
				if u.A.View().Queue.Browsing {
					u.A.Q.EndBrowse()
				} else {
					u.A.Q.Browse()
				}
				u.refresh()
			} else if s.kind == "card" {
				u.A.Open(s.card.Key) // a card with no path has nowhere to go: Open does nothing
			}
		case hitAct1:
			first, _, _ := talkButtons(s.card)
			u.A.OpenAction(s.card.Key, first) // the card stays: the call goes on
		case hitAct2:
			_, second, _ := talkButtons(s.card)
			u.A.OpenAction(s.card.Key, second)
		case hitActMenu:
			u.actionMenu(s)
		case hitHideAll:
			if u.asking != 0 { // «Отмена»
				u.stopAsking()
				break
			}
			u.A.HideAll()
		case hitReadAll:
			u.readAll()
		case hitLogin:
			u.A.Login(u.Ctx)
		}
		return 0
	case wmRButtonUp:
		if s != nil && s.kind == "more" {
			u.stripMenu()
		}
		return 0
	case wmMouseWheel:
		if hiword(wp) > 0 {
			u.A.Q.Scroll(-1)
		} else {
			u.A.Q.Scroll(1)
		}
		u.refresh()
		return 0
	case wmDpiChanged:
		u.refresh()
		return 0
	case wmDropFiles:
		u.dropped(s, wp)
		return 0
	case wmSetFocus:
		if s != nil && s == u.rep.host && u.rep.edit != 0 {
			pSetFocus.Call(u.rep.edit)
			return 0
		}
	case wmClose:
		return 0 // Alt+F4 on a card does nothing: the card stays until an action
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// ---- painting ----

var (
	colBg      = rgb(0xFF, 0xFF, 0xFF)
	colBorder  = rgb(0xDD, 0xE3, 0xEA)
	colTitle   = rgb(0x14, 0x18, 0x1D)
	colBody    = rgb(0x45, 0x4C, 0x55)
	colMuted   = rgb(0x8A, 0x93, 0x9E)
	colBlue    = rgb(0x33, 0x90, 0xEC)
	colGreen   = rgb(0x2E, 0xAD, 0x5B)
	colOrange  = rgb(0xF0, 0x9A, 0x1A)
	colBtn     = rgb(0xEE, 0xF1, 0xF5)
	colBtnHov  = rgb(0xE1, 0xE6, 0xEC)
	colBlueHov = rgb(0x24, 0x7C, 0xD6)
	colHint    = rgb(0x2B, 0x31, 0x38)
	colMoreHov = rgb(0x45, 0x4C, 0x55)
	colMoreBg  = rgb(0x2B, 0x31, 0x38)
	colWhite   = rgb(0xFF, 0xFF, 0xFF)
	// the colours of the CRM (logo.go): the band and the main buttons of the windows
	colNavy    = rgb(0x19, 0x37, 0x5F)
	colNavyHov = rgb(0x24, 0x45, 0x74)
	colNavyOff = rgb(0xA9, 0xB4, 0xC2)
	colSilver  = rgb(0xC4, 0xCB, 0xD4)
	colNavyBg  = rgb(0xDC, 0xE4, 0xEF) // the chosen place in the settings
)

func (u *UI) font(size int, bold bool) uintptr {
	weight := 400
	if bold {
		weight = 600
	}
	return u.fontOf("Segoe UI", size, weight)
}

// symbolFont has the glyphs of the icons on every Windows 10/11.
func (u *UI) symbolFont(size int) uintptr { return u.fontOf("Segoe UI Symbol", size, 400) }

func (u *UI) fontOf(family string, size, weight int) uintptr {
	key := fmt.Sprint(family, size, weight, u.scale)
	if f, ok := u.fonts[key]; ok {
		return f
	}
	f, _, _ := pCreateFontW.Call(uintptr(-u.pi(size)), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 5, 0,
		uintptr(unsafe.Pointer(utf16(family))))
	u.fonts[key] = f
	return f
}

type painter struct {
	u  *UI
	dc uintptr
}

func (p painter) fill(r rect, color uintptr) {
	b, _, _ := pCreateSolidBrush.Call(color)
	pFillRect.Call(p.dc, ptr(&r), b)
	pDeleteObject.Call(b)
}

func (p painter) round(r rect, fill, border uintptr, rad int32) {
	b, _, _ := pCreateSolidBrush.Call(fill)
	pen, _, _ := pCreatePen.Call(psSolid, 1, border)
	ob, _, _ := pSelectObject.Call(p.dc, b)
	op, _, _ := pSelectObject.Call(p.dc, pen)
	pRoundRect.Call(p.dc, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right), uintptr(r.Bottom), uintptr(rad), uintptr(rad))
	pSelectObject.Call(p.dc, ob)
	pSelectObject.Call(p.dc, op)
	pDeleteObject.Call(b)
	pDeleteObject.Call(pen)
}

func (p painter) text(s string, r rect, font, color uintptr, flags uintptr) {
	if s == "" {
		return
	}
	pSelectObject.Call(p.dc, font)
	pSetTextColor.Call(p.dc, color)
	pDrawTextW.Call(p.dc, uintptr(unsafe.Pointer(utf16(s))), ^uintptr(0), ptr(&r), flags|dtNoPrefix)
}

func accent(c api.Card) uintptr {
	switch {
	case c.Kind == queue.KindCall:
		return colGreen
	case c.Alert:
		return colRed
	case c.Kind == queue.KindDelivery:
		return colDelivery
	case c.From == api.FromStaff:
		return colViolet
	case c.Mention:
		return colOrange
	case c.Sound == api.SoundClient:
		return colGreen
	}
	return colBlue
}

// buffered paints a window through a bitmap, so it never shows half drawn.
func (u *UI) buffered(hwnd uintptr, draw func(p painter, cr rect)) {
	var ps paintStruct
	hdc, _, _ := pBeginPaint.Call(hwnd, ptr(&ps))
	defer pEndPaint.Call(hwnd, ptr(&ps))
	var cr rect
	pGetClientRect.Call(hwnd, ptr(&cr))
	mem, _, _ := pCreateCompatibleDC.Call(hdc)
	bmp, _, _ := pCreateCompatibleBitmap.Call(hdc, uintptr(cr.Right), uintptr(cr.Bottom))
	old, _, _ := pSelectObject.Call(mem, bmp)
	pSetBkMode.Call(mem, transparentMode)
	draw(painter{u: u, dc: mem}, cr)
	pBitBlt.Call(hdc, 0, 0, uintptr(cr.Right), uintptr(cr.Bottom), mem, 0, 0, srcCopy)
	pSelectObject.Call(mem, old)
	pDeleteObject.Call(bmp)
	pDeleteDC.Call(mem)
}

func (u *UI) paint(s *slot) {
	u.buffered(s.hwnd, func(p painter, cr rect) { u.drawCard(p, s, cr) })
}

func (u *UI) drawCard(p painter, s *slot, cr rect) {
	w, h := cr.Right, cr.Bottom
	if s.kind == "more" {
		p.fill(cr, colMoreBg)
		hide, read := u.stripButtons(s)
		p.text(s.text, rect{u.pi(14), 0, hide.Left - u.pi(6), h}, u.font(12, false), colWhite, dtSingleLine|dtVCenter|dtEndEllipsis)
		for _, b := range []struct {
			r     rect
			label string
			hit   int
		}{{hide, "Скрыть все", hitHideAll}, {read, "Прочитать все", hitReadAll}} {
			if u.asking != 0 {
				b.label = map[int]string{hitHideAll: "Отмена", hitReadAll: "Да, прочитать"}[b.hit]
				if b.hit == hitReadAll { // the answer that cannot be undone stands out
					p.round(rect{b.r.Left, b.r.Top + u.pi(4), b.r.Right, b.r.Bottom - u.pi(4)}, colRed, colRed, u.pi(8))
				}
			}
			if s.hover == b.hit && !(u.asking != 0 && b.hit == hitReadAll) {
				p.round(rect{b.r.Left, b.r.Top + u.pi(4), b.r.Right, b.r.Bottom - u.pi(4)}, colMoreHov, colMoreHov, u.pi(8))
			}
			p.text(b.label, b.r, u.font(12, true), colWhite, dtSingleLine|dtVCenter|dtCenter)
		}
		return
	}
	p.round(rect{0, 0, w, h}, colBg, colBorder, u.pi(radius))
	if service(s) {
		u.drawService(p, s, w, h)
		return
	}
	p.fill(rect{0, u.pi(10), u.pi(4), h - u.pi(10)}, accent(s.card))
	open, read, cl := u.buttons(s)
	left := u.pi(14)
	call := s.card.Kind == queue.KindCall
	if call { // a call is now: a telephone in place of the time
		p.text("☎", rect{left, 0, left + u.pi(24), u.pi(36)}, u.symbolFont(16), colGreen, dtSingleLine|dtVCenter)
		left += u.pi(28)
	} else if t := clock(s.card.At); t != "" {
		p.text(t, rect{left, 0, left + u.pi(38), u.pi(36)}, u.font(12, false), colMuted, dtSingleLine|dtVCenter)
		left += u.pi(42)
	}
	left = u.badges(p, s.card, left)
	first := cl.Left // the leftmost circle
	for _, r := range []rect{open, read} {
		if r.Right > 0 {
			first = r.Left
		}
	}
	title := colTitle
	if s.card.Alert {
		title = colRed
	}
	p.text(cardTitle(s.card), rect{left, 0, first - u.pi(6), u.pi(36)}, u.font(13, true), title, dtSingleLine|dtVCenter|dtEndEllipsis)
	if s.card.Talking() { // «Разговор»: the number on one line, the buttons in words below
		p.text(s.card.Body, rect{u.pi(14), u.pi(34), w - u.pi(12), u.pi(56)}, u.font(13, false), colBody, dtSingleLine|dtVCenter|dtEndEllipsis)
		u.drawTalkButtons(p, s)
	} else if s.pic != nil { // the sticker in place of the text; «Иван: Стикер» of a group stays next to it
		size := u.pi(stickerPx)
		p.pixels(u.stickerPixels(s.pic, size), u.pi(14), u.pi(stickerTop), size, size)
		if s.card.Body != stickerBody {
			x := u.pi(14) + size + u.pi(12)
			p.text(s.card.Body, rect{x, u.pi(stickerTop), w - u.pi(12), u.pi(stickerTop) + size}, u.font(13, false), colMuted, dtWordBreak|dtEndEllipsis|dtEditControl)
		}
	} else {
		p.text(s.card.Body, rect{u.pi(14), u.pi(34), w - u.pi(12), u.pi(70)}, u.font(13, false), colBody, dtWordBreak|dtEndEllipsis|dtEditControl)
	}
	if s.card.Reply != nil {
		u.drawReply(p, s)
	}
	u.circle(p, s, read, "✓", hitRead, dotGreen)
	u.circle(p, s, open, "↗", hitOpen, dotBlue)
	u.circle(p, s, cl, "✕", hitClose, dotRed)
	hint := hintOf(s.hover)
	if call && s.hover == hitClose {
		hint = "Пропустить" // only here: the call goes on
	}
	if hint != "" { // the words of the circle under the mouse, over the end of the title
		at := rect{first - u.pi(118), u.pi(8), first - u.pi(6), u.pi(8 + dot)}
		p.round(at, colHint, colHint, u.pi(8))
		p.text(hint, at, u.font(12, false), colWhite, dtSingleLine|dtVCenter|dtCenter)
	}
}

// drawTalkButtons: «Карточка клиента» in grey, the main «Написать в …» in blue, «▾» with the rest.
func (u *UI) drawTalkButtons(p painter, s *slot) {
	first, second, _ := talkButtons(s.card)
	b1, b2, bm := u.talkRects(s)
	button := func(r rect, i, hit int) {
		if r.Right == 0 || i < 0 {
			return
		}
		a := s.card.Actions[i]
		bg, hov, fg, bold := colBtn, colBtnHov, colTitle, false
		if a.ID == "message" {
			bg, hov, fg, bold = colBlue, colBlueHov, colWhite, true
		}
		if s.hover == hit {
			bg = hov
		}
		p.round(r, bg, bg, u.pi(8))
		p.text(a.Title, rect{r.Left + u.pi(8), r.Top, r.Right - u.pi(8), r.Bottom}, u.font(12, bold), fg, dtSingleLine|dtVCenter|dtCenter|dtEndEllipsis)
	}
	button(b1, first, hitAct1)
	button(b2, second, hitAct2)
	if bm.Right > 0 {
		bg := colBtn
		if s.hover == hitActMenu {
			bg = colBtnHov
		}
		p.round(bm, bg, bg, u.pi(8))
		p.text("▾", bm, u.symbolFont(11), colTitle, dtSingleLine|dtVCenter|dtCenter)
	}
}

// circle is a button of the first line of a card: a smooth coloured circle with a sign (not only the colour tells them
// apart), drawn by dotImage. An empty rect — no such button on this card.
func (u *UI) circle(p painter, s *slot, r rect, sign string, hit int, st dotStyle) {
	if r.Right == 0 {
		return
	}
	size := u.pi(dotDraw)
	hover := s.hover == hit
	key := fmt.Sprint(st.fill, sign, hover, size)
	if u.dots == nil {
		u.dots = map[string][]byte{}
	}
	pix, ok := u.dots[key]
	if !ok {
		pix = bgra(dotImage(int(size), st, sign, hover, rgba(0xFF, 0xFF, 0xFF))) // the card is always white
		u.dots[key] = pix
	}
	p.pixels(pix, r.Left+(r.Right-r.Left-size)/2, r.Top+(r.Bottom-r.Top-size)/2, size, size)
}

// stickerPixels is a sticker fitted to size, kept for the next paints.
func (u *UI) stickerPixels(img image.Image, size int32) []byte {
	type key struct {
		img  image.Image
		size int32
	}
	k := key{img, size}
	if pix, ok := u.pics[k]; ok {
		return pix
	}
	if u.pics == nil || len(u.pics) > 64 {
		u.pics = map[any][]byte{}
	}
	pix := bgra(fitted(img, int(size)))
	u.pics[k] = pix
	return pix
}

// pixels puts a picture drawn by the app (bgra) at x, y.
func (p painter) pixels(pix []byte, x, y, w, h int32) {
	bi := bitmapInfo{Width: w, Height: -h, Planes: 1, BitCount: 32}
	bi.Size = uint32(unsafe.Sizeof(bi))
	pSetDIBitsToDevice.Call(p.dc, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 0, 0, 0, uintptr(h),
		uintptr(unsafe.Pointer(&pix[0])), ptr(&bi), 0)
}

// header is the band of the windows of the app in the colours of the CRM: the sign, «AshCRM Notify» and what the
// window is for.
func (u *UI) header(p painter, w int32, what string) {
	h := u.pi(headH)
	p.fill(rect{0, 0, w, h}, colNavy)
	size := u.pi(36)
	key := fmt.Sprint("logo", size)
	if u.dots == nil {
		u.dots = map[string][]byte{}
	}
	pix, ok := u.dots[key]
	if !ok {
		pix = bgra(onto(Logo(int(size), LogoOptions{}), navyMid))
		u.dots[key] = pix
	}
	left := u.pi(20)
	p.pixels(pix, left, (h-size)/2, size, size)
	x := left + size + u.pi(12)
	p.text("AshCRM Notify", rect{x, u.pi(10), w - u.pi(16), u.pi(32)}, u.font(15, true), colWhite, dtSingleLine|dtVCenter)
	p.text(what, rect{x, u.pi(31), w - u.pi(16), u.pi(52)}, u.font(12, false), colSilver, dtSingleLine|dtVCenter)
}

// hintOf is the word of a circle.
func hintOf(hit int) string {
	switch hit {
	case hitRead:
		return "Прочитано"
	case hitOpen:
		return "Открыть в CRM"
	case hitClose:
		return "Закрыть"
	}
	return ""
}

// drawService is the card «Войдите» or «Доступна версия»: a decision, so its buttons are in words.
func (u *UI) drawService(p painter, s *slot, w, h int32) {
	title, body := s.card.Title, s.card.Body
	if s.kind == "signin" {
		title, body = "AshCRM Notify", "Войдите, чтобы получать уведомления о сообщениях и беседах."
		if s.text != "" {
			body = s.text
		}
	}
	p.fill(rect{0, u.pi(10), u.pi(4), h - u.pi(10)}, colBlue)
	left := u.pi(18)
	p.text(title, rect{left, u.pi(12), w - u.pi(38), u.pi(34)}, u.font(14, true), colTitle, dtSingleLine|dtEndEllipsis)
	p.text(body, rect{left, u.pi(36), w - u.pi(16), u.pi(74)}, u.font(13, false), colBody, dtWordBreak|dtEndEllipsis|dtEditControl)
	open, read, cl := u.buttons(s)
	xc := colMuted
	if s.hover == hitClose {
		p.round(cl, colBtnHov, colBtnHov, u.pi(8))
		xc = colTitle
	}
	p.text("✕", cl, u.font(12, false), xc, dtSingleLine|dtVCenter|dtCenter)
	if s.kind == "signin" {
		bg := colBlue
		if s.hover == hitLogin {
			bg = colBlueHov
		}
		p.round(read, bg, bg, u.pi(8))
		p.text("Войти", read, u.font(13, true), colWhite, dtSingleLine|dtVCenter|dtCenter)
		return
	}
	ob := colBtn
	if s.hover == hitOpen {
		ob = colBtnHov
	}
	label := "Обновить"
	if u.A.View().Updating {
		label = "Обновляю…"
	}
	p.round(open, ob, ob, u.pi(8))
	p.text("Позже", open, u.font(13, false), colTitle, dtSingleLine|dtVCenter|dtCenter)
	rb := colBlue
	if s.hover == hitRead {
		rb = colBlueHov
	}
	p.round(read, rb, rb, u.pi(8))
	p.text(label, read, u.font(13, true), colWhite, dtSingleLine|dtVCenter|dtCenter)
}
