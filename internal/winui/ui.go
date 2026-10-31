//go:build windows

// Package winui is the face of the app on Windows: the tray icon with its menu, up to three cards for a few seconds and
// a strip with the number of unread ones at the edge of the screen (docs/APP_SPEC.md §3.1, §3.4). It draws with plain
// Win32/GDI — no runtime to ship.
package winui

import (
	"context"
	"fmt"
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
	cardW    = 360
	cardH    = 76  // the line of the sender and two lines of the message
	serviceH = 116 // the cards with worded buttons
	moreH    = 30
	iconSize = 24
	gap      = 8
	margin   = 12
	radius   = 14
	slotMax  = queue.Max + 1 // three cards and the strip of the unread
)

const trayClassName = "AshCRMNotifyTray"

// timerHide wakes the UI when the next card must leave the screen.
const timerHide = 1

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
)

// what a click hits on a card
const (
	hitNone = iota
	hitBody
	hitOpen
	hitRead
	hitClose
	hitLogin
)

// slot is one window of the stack: a card, the «sign in» card or the strip of the unread.
type slot struct {
	hwnd  uintptr
	card  api.Card
	kind  string // card | signin | more
	text  string // the strip / the error of the sign-in card
	hover int
	shown bool
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
	signinOK  bool // the person closed the «sign in» card

	portal       portal
	portalAsked  bool
	portalResult atomic.Pointer[error]

	settings settings
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
	r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16("AshCRM Notify"))), 0x4|0x20) // YESNO|QUESTION
	return r == 6                                                                                                                    // IDYES
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
			0, wsPopup, 0, 0, u.px(cardW), u.px(cardH), 0, 0, u.inst, 0)
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
	key := "off"
	switch {
	case v.State != app.Online:
	case v.DND:
		key = "dnd"
	default:
		key = "on"
	}
	if v.Queue.Unread > 0 {
		key += "-dot"
	}
	size := int(16*u.scale + 0.5)
	if size < 16 {
		size = 16
	}
	cached := fmt.Sprint(key, size)
	if h, ok := u.icons[cached]; ok {
		return key, h
	}
	bg := ColorOffline
	if strings.HasPrefix(key, "on") {
		bg = ColorOnline
	} else if strings.HasPrefix(key, "dnd") {
		bg = ColorDND
	}
	png := IconPNG(size*2, bg, strings.HasSuffix(key, "-dot"))
	h, _, _ := pCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&png[0])), uintptr(len(png)), 1, 0x00030000,
		uintptr(size), uintptr(size), 0)
	u.icons[cached] = h
	return key, h
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
	case wmTimer:
		if wp == timerHide {
			pKillTimer.Call(u.tray, timerHide)
			u.A.Q.Tick(time.Now())
			u.refresh()
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
	if u.settings.hwnd != 0 {
		pInvalidateRect.Call(u.settings.hwnd, 0, 0) // the result of the check
	}
	d := u.trayData(v)
	pShellNotifyIconW.Call(nimModify, ptr(&d))
	type want struct {
		kind, text string
		card       api.Card
	}
	var list []want
	if t := stripText(v.Queue); t != "" {
		list = append(list, want{kind: "more", text: t})
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
		list = append(list, want{kind: "card", card: c})
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
		default:
			heights[i] = u.pi(cardH)
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
		changed := rescaled || s.kind != w.kind || s.text != w.text || s.card != w.card
		s.kind, s.text, s.card = w.kind, w.text, w.card
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
	if !service(s) { // the icons of the first line: «Прочитано», «Открыть», «✕»
		size, top, step := u.pi(iconSize), u.pi(6), u.pi(iconSize+2)
		close = rect{w - u.pi(8) - size, top, w - u.pi(8), top + size}
		open = rect{close.Left - step, top, close.Right - step, top + size}
		read = rect{open.Left - step, top, open.Right - step, top + size}
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

func (u *UI) hit(s *slot, x, y int32) int {
	if s.kind == "more" {
		return hitBody
	}
	open, read, cl := u.buttons(s)
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

// stripMenu is the right click on the strip.
func (u *UI) stripMenu() {
	n := u.A.View().Queue.Unread
	h, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(h)
	pAppendMenuW.Call(h, mfString, cmdOpenCRM, uintptr(unsafe.Pointer(utf16("Открыть AshCRM"))))
	pAppendMenuW.Call(h, mfString, cmdReadAll, uintptr(unsafe.Pointer(utf16(fmt.Sprintf("Прочитать все (%d)", n)))))
	var p point
	pGetCursorPos.Call(ptr(&p))
	pSetForegroundWindow.Call(u.tray) // else the menu does not close on a click outside
	cmd, _, _ := pTrackPopupMenu.Call(h, tpmRightButton|tpmReturnCmd, uintptr(p.X), uintptr(p.Y), 0, u.tray, 0)
	pPostMessageW.Call(u.tray, wmNull, 0, 0)
	switch int(cmd) {
	case cmdOpenCRM:
		u.A.OpenCRM()
	case cmdReadAll:
		if confirm(fmt.Sprintf("Отметить прочитанными все уведомления (%d)? Отменить это нельзя.", n)) {
			u.A.ReadAll(u.Ctx)
		}
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
		switch u.hit(s, loword(lp), hiword(lp)) {
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
				u.A.Open(s.card.Key)
			}
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
	colMoreBg  = rgb(0x2B, 0x31, 0x38)
	colWhite   = rgb(0xFF, 0xFF, 0xFF)
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
		p.text(s.text, rect{u.pi(14), 0, w - u.pi(14), h}, u.font(12, false), colWhite, dtSingleLine|dtVCenter|dtEndEllipsis|dtCenter)
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
	if t := clock(s.card.At); t != "" {
		p.text(t, rect{left, 0, left + u.pi(38), u.pi(36)}, u.font(12, false), colMuted, dtSingleLine|dtVCenter)
		left += u.pi(42)
	}
	p.text(cardTitle(s.card), rect{left, 0, read.Left - u.pi(4), u.pi(36)}, u.font(13, true), colTitle, dtSingleLine|dtVCenter|dtEndEllipsis)
	p.text(s.card.Body, rect{u.pi(14), u.pi(34), w - u.pi(12), u.pi(70)}, u.font(13, false), colBody, dtWordBreak|dtEndEllipsis|dtEditControl)
	u.icon(p, s, read, "✓", hitRead, colBlue)
	u.icon(p, s, open, "↗", hitOpen, colBody)
	u.icon(p, s, cl, "✕", hitClose, colMuted)
}

// icon is a button of the first line of a card.
func (u *UI) icon(p painter, s *slot, r rect, glyph string, hit int, color uintptr) {
	if s.hover == hit {
		p.round(r, colBtnHov, colBtnHov, u.pi(8))
		color = colTitle
	}
	p.text(glyph, r, u.symbolFont(14), color, dtSingleLine|dtVCenter|dtCenter)
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
