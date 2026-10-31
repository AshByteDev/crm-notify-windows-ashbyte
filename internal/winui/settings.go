//go:build windows

package winui

import (
	"strings"
	"syscall"
	"unsafe"
)

// «Настройки»: a picture of a monitor, where a click on a part of its screen chooses the place of the cards at once,
// and the check of the whole way: the account, the CRM, a card, a sound.

const (
	settingsW = 440
	settingsH = 432

	hitTest = "test" // the button of the check (the other hits are places of the cards)
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

func (u *UI) testButton() rect {
	mid := u.pi(settingsW) / 2
	return rect{mid - u.pi(120), u.pi(336), mid + u.pi(120), u.pi(368)}
}

// settingsHit is what lies under the mouse: a place of the cards, the button or nothing.
func (u *UI) settingsHit(x, y int32) string {
	if in(u.testButton(), x, y) {
		return hitTest
	}
	_, screen := u.monitor()
	return zoneAt(screen, x, y)
}

func (u *UI) settingsProc(hwnd, m, wp, lp uintptr) uintptr {
	switch uint32(m) {
	case wmPaint:
		u.buffered(hwnd, u.drawSettings)
		return 0
	case wmEraseBkgnd:
		return 1 // the whole window is painted at once, without flicker
	case wmMouseMove:
		if z := u.settingsHit(loword(lp), hiword(lp)); z != u.settings.hover {
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
		switch z := u.settingsHit(loword(lp), hiword(lp)); z {
		case "":
		case hitTest:
			go u.A.TestNotify(u.Ctx)
		default:
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
	v := u.A.View()
	chosen := v.Position
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

	btn, bg := u.testButton(), colBlue
	if u.settings.hover == hitTest {
		bg = colBlueHov
	}
	p.round(btn, bg, bg, u.pi(8))
	p.text("Проверить уведомления", btn, u.font(13, true), colWhite, dtSingleLine|dtVCenter|dtCenter)
	result := v.Test
	if strings.TrimSpace(result) == "" {
		result = "Покажет карточку со звуком и сотрудника, которому приходят уведомления."
	}
	p.text(result, rect{left, u.pi(376), right, u.pi(424)}, u.font(12, false), colBody, dtCenter|dtWordBreak|dtEndEllipsis|dtEditControl)
}
