//go:build windows

package winui

import (
	"syscall"
	"unsafe"
)

// The first start: «Адрес портала AshCRM» — a small window with a field; the address is checked (it must be an
// AshCRM that knows the app), then the sign-in opens in the browser. Also «Сменить портал…» in the menu.

const (
	idEdit  = 101
	idError = 102
	portalW = 440
	portalH = 200 + headH
)

type portal struct {
	hwnd, edit, button, errText uintptr
	err                         string
	busy                        bool
}

// whiteBrush paints the window and its texts white, as the cards and the settings.
var whiteBrush uintptr

func (u *UI) registerPortal() error {
	c := wndClassEx{WndProc: syscall.NewCallback(u.portalProc), Instance: u.inst, ClassName: utf16("AshCRMNotifyPortal")}
	whiteBrush, _, _ = pCreateSolidBrush.Call(colBg)
	c.Background = whiteBrush
	c.Cursor, _, _ = pLoadCursorW.Call(0, idcArrow)
	c.Icon, c.IconSm = u.windowIcons()
	c.Size = uint32(unsafe.Sizeof(c))
	if r, _, err := pRegisterClassExW.Call(ptr(&c)); r == 0 {
		return err
	}
	return nil
}

func (u *UI) child(class, text string, style uintptr, ex uintptr, x, y, w, h int, id int) uintptr {
	hw, _, _ := pCreateWindowExW.Call(ex, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(text))),
		wsChild|wsVisible|style, uintptr(u.pi(x)), uintptr(u.pi(y)), uintptr(u.pi(w)), uintptr(u.pi(h)), u.portal.hwnd, uintptr(id), u.inst, 0)
	pSendMessageW.Call(hw, wmSetFont, u.font(13, false), 1)
	return hw
}

// centered creates a hidden window with a caption in the middle of the screen; w and h are the logical client size.
func (u *UI) centered(class, title string, w, h int) uintptr {
	style := uintptr(wsOverlapped | wsCaption | wsSysMenu | wsClipChildren)
	r := rect{0, 0, u.pi(w), u.pi(h)}
	pAdjustWindowRectEx.Call(ptr(&r), style, 0, 0)
	fw, fh := r.Right-r.Left, r.Bottom-r.Top
	sw, _, _ := pGetSystemMetrics.Call(smCxScreen)
	sh, _, _ := pGetSystemMetrics.Call(smCyScreen)
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(title))), style,
		uintptr((int32(sw)-fw)/2), uintptr((int32(sh)-fh)/2), uintptr(fw), uintptr(fh), 0, 0, u.inst, 0)
	return hwnd
}

// showPortal opens (or brings back) the window of the address.
func (u *UI) showPortal(prefill string) {
	if u.portal.hwnd != 0 {
		pShowWindow.Call(u.portal.hwnd, swShow)
		pSetForegroundWindow.Call(u.portal.hwnd)
		return
	}
	u.portal.hwnd = u.centered("AshCRMNotifyPortal", "AshCRM Notify — подключение", portalW, portalH)
	top := headH // under the band with the sign
	title := u.child("STATIC", "Адрес портала AshCRM", 0, 0, 20, top+16, 400, 22, 0)
	pSendMessageW.Call(title, wmSetFont, u.font(15, true), 1)
	u.child("STATIC", "Тот же адрес, по которому вы открываете CRM в браузере, например crm.company.ru", 0, 0, 20, top+42, 400, 36, 0)
	u.portal.edit = u.child("EDIT", prefill, wsTabStop|wsBorder|esAutoHScroll, 0, 20, top+84, 400, 28, idEdit)
	u.portal.errText = u.child("STATIC", "", 0, 0, 20, top+120, 400, 36, idError)
	// drawn by the app in the colours of the CRM; Enter still presses it (dmGetDefID)
	u.portal.button = u.child("BUTTON", "Подключиться", wsTabStop|bsOwnerDraw, 0, 280, top+156, 140, 32, idOK)
	pShowWindow.Call(u.portal.hwnd, swShow)
	pSetForegroundWindow.Call(u.portal.hwnd)
	pSetFocus.Call(u.portal.edit)
	pSendMessageW.Call(u.portal.edit, emSetSel, 0, ^uintptr(0))
}

func (u *UI) closePortal() {
	if u.portal.hwnd != 0 {
		pDestroyWindow.Call(u.portal.hwnd)
		u.portal = portal{}
	}
}

func windowText(h uintptr) string {
	buf := make([]uint16, 512)
	n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf[:n])
}

func (u *UI) portalError(text string) {
	u.portal.err = text
	pSetWindowTextW.Call(u.portal.errText, uintptr(unsafe.Pointer(utf16(text))))
}

func (u *UI) submitPortal() {
	if u.portal.busy {
		return
	}
	addr := windowText(u.portal.edit)
	u.portal.busy = true
	pEnableWindow.Call(u.portal.button, 0)
	u.portalError("Проверяю адрес…")
	go func() {
		err := u.A.SetServer(u.Ctx, addr)
		u.portalResult.Store(&err)
		u.Notify()
		if h := u.trayReady.Load(); h != 0 {
			pPostMessageW.Call(h, wmPortalDone, 0, 0)
		}
	}()
}

// portalDone runs on the UI thread with the result of the check.
func (u *UI) portalDone() {
	p := u.portalResult.Swap(nil)
	if p == nil || u.portal.hwnd == 0 {
		return
	}
	u.portal.busy = false
	pEnableWindow.Call(u.portal.button, 1)
	if *p != nil {
		u.portalError((*p).Error())
		pSetFocus.Call(u.portal.edit)
		return
	}
	u.closePortal() // the sign-in continues in the browser
}

func (u *UI) portalProc(hwnd, m, wp, lp uintptr) uintptr {
	switch uint32(m) {
	case wmCommand:
		if wp&0xFFFF == idOK {
			u.submitPortal()
			return 0
		}
	case wmPaint:
		u.buffered(hwnd, func(p painter, cr rect) {
			p.fill(cr, colBg)
			u.header(p, cr.Right, "Подключение к порталу")
		})
		return 0
	case wmEraseBkgnd:
		return 1
	case wmDrawItem:
		u.drawPortalButton(*(**drawItem)(unsafe.Pointer(&lp))) // lp points at DRAWITEMSTRUCT
		return 1
	case dmGetDefID:
		return idOK | dcHasDefID<<16
	case wmCtlColorStatic:
		color := colBody
		if lp == u.portal.errText && u.portal.err != "" && !u.portal.busy {
			color = rgb(0xD0, 0x30, 0x30)
		}
		pSetTextColor.Call(wp, color)
		pSetBkMode.Call(wp, transparentMode)
		return whiteBrush
	case wmClose:
		pShowWindow.Call(hwnd, swHide) // the app stays in the tray; a click on the icon brings the window back
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
}

// drawPortalButton paints «Подключиться»: navy as the main buttons of the CRM, pale while the address is checked.
func (u *UI) drawPortalButton(d *drawItem) {
	p := painter{u: u, dc: d.DC}
	pSetBkMode.Call(d.DC, transparentMode)
	p.fill(d.Rect, colBg)
	bg := colNavy
	switch {
	case d.State&odsDisabled != 0:
		bg = colNavyOff
	case d.State&odsSelected != 0:
		bg = colNavyHov
	}
	p.round(d.Rect, bg, bg, u.pi(8))
	p.text(windowText(d.Item), d.Rect, u.font(13, true), colWhite, dtSingleLine|dtVCenter|dtCenter)
}
