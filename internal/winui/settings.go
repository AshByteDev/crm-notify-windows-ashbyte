//go:build windows

package winui

import (
	"syscall"
	"unsafe"
)

// «Настройки»: a picture of a monitor; a click on a part of its screen chooses where the cards are shown, at once.

const (
	settingsW = 440
	settingsH = 344
)

var (
	colBezel    = rgb(0x2B, 0x31, 0x38)
	colScreen   = rgb(0xF3, 0xF5, 0xF8)
	colZoneHov  = rgb(0xE1, 0xE6, 0xEC)
	colZoneOn   = rgb(0xD6, 0xE9, 0xFB)
	colMiniCard = rgb(0xB8, 0xC1, 0xCB)
)

type settings struct {
	hwnd  uintptr
	hover string
}

func (u *UI) registerSettings() error {
	c := wndClassEx{WndProc: syscall.NewCallback(u.settingsProc), Instance: u.inst, ClassName: utf16("AshCRMNotifySettings")}
	c.Cursor, _, _ = pLoadCursorW.Call(0, idcArrow)
	c.Size = uint32(unsafe.Sizeof(c))
	if r, _, err := pRegisterClassExW.Call(ptr(&c)); r == 0 {
		return err
	}
	return nil
}

// showSettings opens (or brings forward) the window.
func (u *UI) showSettings() {
	if u.settings.hwnd == 0 {
		u.settings.hwnd = u.centered("AshCRMNotifySettings", "AshCRM Notify — настройки", settingsW, settingsH)
	}
	pShowWindow.Call(u.settings.hwnd, swShow)
	pSetForegroundWindow.Call(u.settings.hwnd)
}

// monitor is the body of the monitor in the picture, screen — the clickable part of it.
func (u *UI) monitor() (body, screen rect) {
	body = rect{u.pi(60), u.pi(72), u.pi(380), u.pi(272)}
	b := u.pi(8)
	return body, rect{body.Left + b, body.Top + b, body.Right - b, body.Bottom - b}
}

func (u *UI) settingsProc(hwnd, m, wp, lp uintptr) uintptr {
	switch uint32(m) {
	case wmPaint:
		u.buffered(hwnd, u.drawSettings)
		return 0
	case wmEraseBkgnd:
		return 1 // the whole window is painted at once, without flicker
	case wmMouseMove:
		_, screen := u.monitor()
		if z := zoneAt(screen, loword(lp), hiword(lp)); z != u.settings.hover {
			u.settings.hover = z
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		t := trackMouseEvent{Flags: tmeLeave, Track: hwnd}
		t.Size = uint32(unsafe.Sizeof(t))
		pTrackMouseEvent.Call(ptr(&t))
		return 0
	case wmMouseLeave:
		u.settings.hover = ""
		pInvalidateRect.Call(hwnd, 0, 0)
		return 0
	case wmSetCursor:
		if u.settings.hover != "" && lp&0xFFFF == htClient {
			hand, _, _ := pLoadCursorW.Call(0, idcHand)
			pSetCursor.Call(hand)
			return 1
		}
	case wmLButtonUp:
		_, screen := u.monitor()
		if z := zoneAt(screen, loword(lp), hiword(lp)); z != "" {
			u.A.SetPosition(z)
			pInvalidateRect.Call(hwnd, 0, 0)
		}
		return 0
	case wmClose:
		pDestroyWindow.Call(hwnd)
		u.settings = settings{}
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

func (u *UI) drawSettings(p painter, cr rect) {
	chosen := u.A.View().Position
	p.fill(cr, colBg)
	left, right := u.pi(20), cr.Right-u.pi(20)
	p.text("Где показывать уведомления", rect{left, u.pi(14), right, u.pi(38)}, u.font(15, true), colTitle, dtSingleLine|dtVCenter)
	p.text("Нажмите на нужную часть экрана", rect{left, u.pi(40), right, u.pi(62)}, u.font(13, false), colBody, dtSingleLine|dtVCenter)

	body, screen := u.monitor()
	mid := (body.Left + body.Right) / 2
	p.fill(rect{mid - u.pi(20), body.Bottom, mid + u.pi(20), body.Bottom + u.pi(14)}, colBezel)
	p.fill(rect{mid - u.pi(52), body.Bottom + u.pi(14), mid + u.pi(52), body.Bottom + u.pi(20)}, colBezel)
	p.round(body, colBezel, colBezel, u.pi(10))
	p.fill(screen, colScreen)

	miniW, miniH, pad, miniGap := u.pi(58), u.pi(18), u.pi(8), u.pi(4)
	for _, z := range zones(screen) {
		on := z.position == chosen
		box := rect{z.box.Left + 1, z.box.Top + 1, z.box.Right - 1, z.box.Bottom - 1}
		switch {
		case on:
			p.fill(box, colZoneOn)
		case z.position == u.settings.hover:
			p.fill(box, colZoneHov)
		}
		heights, color := []int32{miniH}, colMiniCard
		if on {
			heights, color = []int32{miniH, miniH}, colBlue // two cards show the way the stack grows
		}
		for _, at := range stack(z.position, z.box, miniW, heights, pad, miniGap) {
			p.round(rect{at.X, at.Y, at.X + miniW, at.Y + miniH}, color, color, u.pi(5))
		}
	}
	p.text("Уведомления: "+zoneName(chosen), rect{left, u.pi(304), right, u.pi(328)}, u.font(13, false), colBody, dtSingleLine|dtVCenter|dtCenter)
}
