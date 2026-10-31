//go:build windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/sound"
)

// protect encrypts with DPAPI for the current Windows user: another user or another computer cannot read the key.
func protect(b []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(b)), Data: unsafe.SliceData(b)}
	var out windows.DataBlob
	name, _ := windows.UTF16PtrFromString(AppName)
	if err := windows.CryptProtectData(&in, name, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, windows.ERROR_INVALID_DATA
	}
	in := windows.DataBlob{Size: uint32(len(b)), Data: unsafe.SliceData(b)}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

var (
	shell32       = windows.NewLazySystemDLL("shell32.dll")
	shellExecuteW = shell32.NewProc("ShellExecuteW")
	winmm         = windows.NewLazySystemDLL("winmm.dll")
	playSoundW    = winmm.NewProc("PlaySoundW")
	user32        = windows.NewLazySystemDLL("user32.dll")

	getLastInputInfo = user32.NewProc("GetLastInputInfo")
	getTickCount     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount")
)

// lastInputInfo is LASTINPUTINFO.
type lastInputInfo struct {
	Size uint32
	Time uint32
}

// Idle is the time since the last keyboard or mouse input. GetLastInputInfo sees only the session of the caller, so on a
// terminal server every copy watches its own employee. Its time is 32-bit milliseconds since boot and is compared in
// 32 bits, which survives the wrap after 49 days; the input is read first, so the clock is never behind it.
func Idle() (time.Duration, bool) {
	in := lastInputInfo{}
	in.Size = uint32(unsafe.Sizeof(in))
	if r, _, _ := getLastInputInfo.Call(uintptr(unsafe.Pointer(&in))); r == 0 {
		return 0, false
	}
	now, _, _ := getTickCount.Call()
	return time.Duration(uint32(now)-in.Time) * time.Millisecond, true
}

// OpenURL opens a page in the default browser.
func OpenURL(u string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, err := windows.UTF16PtrFromString(u)
	if err != nil {
		return err
	}
	r, _, _ := shellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(file)), 0, 0, windows.SW_SHOWNORMAL)
	if r <= 32 {
		return syscall.Errno(r)
	}
	return nil
}

// CleanupOld removes the file left by the previous update.
func CleanupOld() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// Uninstall removes the app from the profile of the user: autostart, the shortcut and, after the app has closed, its
// settings with the session (their log file is still open). The files of the app stay: they may serve other users.
func Uninstall() error {
	if err := SetAutostart("", false); err != nil {
		return err
	}
	if p, err := shortcutPath(); err == nil {
		_ = os.Remove(p)
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true,
		CmdLine: `cmd.exe /c ping -n 4 127.0.0.1 >nul & rmdir /s /q "` + dir + `"`}
	return cmd.Start()
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// Autostart reports whether the app starts with Windows (HKCU\…\Run).
func Autostart() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(AppName)
	return err == nil
}

// SetAutostart switches starting with Windows for the current user; path is the file with the constant name.
func SetAutostart(path string, on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		if err := k.DeleteValue(AppName); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue(AppName, `"`+path+`" --autostart`)
}

const (
	sndAsync     = 0x0001
	sndNoDefault = 0x0002
	sndMemory    = 0x0004
)

// PlaySound plays a sound class (synthesized WAV kept in memory for the whole run: SND_ASYNC reads it later).
func PlaySound(class string) {
	w := sound.WAV(class)
	playSoundW.Call(uintptr(unsafe.Pointer(&w[0])), 0, sndMemory|sndAsync|sndNoDefault)
}
