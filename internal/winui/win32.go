//go:build windows

package winui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The few Win32 calls the app needs (user32, gdi32, shell32), without cgo.

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	pRegisterClassExW              = user32.NewProc("RegisterClassExW")
	pCreateWindowExW               = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                = user32.NewProc("DefWindowProcW")
	pDestroyWindow                 = user32.NewProc("DestroyWindow")
	pShowWindow                    = user32.NewProc("ShowWindow")
	pSetWindowPos                  = user32.NewProc("SetWindowPos")
	pGetMessageW                   = user32.NewProc("GetMessageW")
	pTranslateMessage              = user32.NewProc("TranslateMessage")
	pDispatchMessageW              = user32.NewProc("DispatchMessageW")
	pPostMessageW                  = user32.NewProc("PostMessageW")
	pPostQuitMessage               = user32.NewProc("PostQuitMessage")
	pBeginPaint                    = user32.NewProc("BeginPaint")
	pEndPaint                      = user32.NewProc("EndPaint")
	pInvalidateRect                = user32.NewProc("InvalidateRect")
	pGetClientRect                 = user32.NewProc("GetClientRect")
	pFillRect                      = user32.NewProc("FillRect")
	pDrawTextW                     = user32.NewProc("DrawTextW")
	pLoadCursorW                   = user32.NewProc("LoadCursorW")
	pSetWindowRgn                  = user32.NewProc("SetWindowRgn")
	pCreatePopupMenu               = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                   = user32.NewProc("AppendMenuW")
	pTrackPopupMenu                = user32.NewProc("TrackPopupMenu")
	pDestroyMenu                   = user32.NewProc("DestroyMenu")
	pGetCursorPos                  = user32.NewProc("GetCursorPos")
	pSetForegroundWindow           = user32.NewProc("SetForegroundWindow")
	pSystemParametersInfoW         = user32.NewProc("SystemParametersInfoW")
	pGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
	pGetDpiForWindow               = user32.NewProc("GetDpiForWindow")
	pSetCursor                     = user32.NewProc("SetCursor")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	pRegisterWindowMessageW        = user32.NewProc("RegisterWindowMessageW")
	pCreateIconFromResourceEx      = user32.NewProc("CreateIconFromResourceEx")
	pDestroyIcon                   = user32.NewProc("DestroyIcon")
	pTrackMouseEvent               = user32.NewProc("TrackMouseEvent")
	pSetTimer                      = user32.NewProc("SetTimer")
	pKillTimer                     = user32.NewProc("KillTimer")
	pMessageBoxW                   = user32.NewProc("MessageBoxW")
	pIsDialogMessageW              = user32.NewProc("IsDialogMessageW")
	pSendMessageW                  = user32.NewProc("SendMessageW")
	pGetWindowTextW                = user32.NewProc("GetWindowTextW")
	pSetWindowTextW                = user32.NewProc("SetWindowTextW")
	pEnableWindow                  = user32.NewProc("EnableWindow")
	pSetFocus                      = user32.NewProc("SetFocus")
	pGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	pAdjustWindowRectEx            = user32.NewProc("AdjustWindowRectEx")
	pGetSysColorBrush              = user32.NewProc("GetSysColorBrush")

	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
	pCreatePen              = gdi32.NewProc("CreatePen")
	pSelectObject           = gdi32.NewProc("SelectObject")
	pDeleteObject           = gdi32.NewProc("DeleteObject")
	pCreateFontW            = gdi32.NewProc("CreateFontW")
	pSetBkMode              = gdi32.NewProc("SetBkMode")
	pSetTextColor           = gdi32.NewProc("SetTextColor")
	pRoundRect              = gdi32.NewProc("RoundRect")
	pCreateRoundRectRgn     = gdi32.NewProc("CreateRoundRectRgn")
	pCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	pCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	pBitBlt                 = gdi32.NewProc("BitBlt")
	pDeleteDC               = gdi32.NewProc("DeleteDC")

	pShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")

	pGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	pCreateMutexW     = kernel32.NewProc("CreateMutexW")
	pCloseHandle      = kernel32.NewProc("CloseHandle")
)

const (
	wmDestroy        = 0x0002
	wmPaint          = 0x000F
	wmClose          = 0x0010
	wmEraseBkgnd     = 0x0014
	wmSettingChange  = 0x001A
	wmSetCursor      = 0x0020
	wmTimer          = 0x0113
	wmDisplayChange  = 0x007E
	wmDpiChanged     = 0x02E0
	wmMouseMove      = 0x0200
	wmLButtonUp      = 0x0202
	wmRButtonUp      = 0x0205
	wmMouseLeave     = 0x02A3
	wmPowerBroadcast = 0x0218
	wmNull           = 0x0000
	wmApp            = 0x8000
	wmTray           = wmApp + 1
	wmRefresh        = wmApp + 2

	pbtResumeAutomatic = 0x0012

	wmCommand        = 0x0111
	wmSetFont        = 0x0030
	wmCtlColorStatic = 0x0138
	wmPortalDone     = wmApp + 3
	wsOverlapped     = 0x00000000
	wsCaption        = 0x00C00000
	wsSysMenu        = 0x00080000
	wsChild          = 0x40000000
	wsVisible        = 0x10000000
	wsBorder         = 0x00800000
	wsTabStop        = 0x00010000
	wsExClientEdge   = 0x00000200
	esAutoHScroll    = 0x0080
	bsDefPushButton  = 0x0001
	swShow           = 5
	smCxScreen       = 0
	smCyScreen       = 1
	colorWindow      = 5
	idOK             = 1
	emSetSel         = 0x00B1
	htClient         = 1

	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExToolWindow  = 0x00000080
	wsExNoActivate  = 0x08000000
	csDropShadow    = 0x00020000
	swHide          = 0
	swShowNoActive  = 4
	swpNoActivate   = 0x0010
	swpShowWindow   = 0x0040
	hwndTopmost     = ^uintptr(0) // -1
	idcHand         = 32649
	idcArrow        = 32512
	spiGetWorkArea  = 0x0030
	tmeLeave        = 0x00000002
	transparentMode = 1

	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	mfString    = 0x0000
	mfGrayed    = 0x0001
	mfChecked   = 0x0008
	mfSeparator = 0x0800

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmBottomAlign = 0x0020

	dtLeft         = 0x0000
	dtCenter       = 0x0001
	dtVCenter      = 0x0004
	dtWordBreak    = 0x0010
	dtSingleLine   = 0x0020
	dtNoPrefix     = 0x0800
	dtEndEllipsis  = 0x8000
	dtEditControl  = 0x2000
	dtRight        = 0x0002
	psSolid        = 0
	srcCopy        = 0x00CC0020
	errAlreadyExis = 183
)

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type paintStruct struct {
	Hdc       uintptr
	Erase     int32
	Paint     rect
	Restore   int32
	IncUpdate int32
	Reserved  [32]byte
}

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     uintptr
}

type trackMouseEvent struct {
	Size      uint32
	Flags     uint32
	Track     uintptr
	HoverTime uint32
}

func utf16(s string) *uint16 {
	p, _ := windows.UTF16PtrFromString(s)
	return p
}

func rgb(r, g, b uint8) uintptr { return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16 }

func ptr[T any](v *T) uintptr { return uintptr(unsafe.Pointer(v)) }

func loword(v uintptr) int32 { return int32(int16(v & 0xFFFF)) }
func hiword(v uintptr) int32 { return int32(int16((v >> 16) & 0xFFFF)) }
