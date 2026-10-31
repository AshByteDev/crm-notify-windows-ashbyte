//go:build windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
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
)

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

// Replace puts the downloaded version in place of the running file (a running .exe cannot be overwritten but can
// be renamed) and starts it; the caller quits. The old file is removed by the next start (CleanupOld).
func Replace(running, downloaded string) error {
	if err := Swap(running, downloaded); err != nil {
		return err
	}
	return exec.Command(running, "--updated").Start()
}

// CleanupOld removes the file left by the previous update.
func CleanupOld() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

// Uninstall removes the app from the profile of the user: autostart, the shortcut, the settings and, after the app has
// closed, the folder of the copy (a running file cannot be removed by itself).
func Uninstall() error {
	if err := SetAutostart(false); err != nil {
		return err
	}
	if p, err := shortcutPath(); err == nil {
		_ = os.Remove(p)
	}
	home, err := Home()
	if err != nil {
		return err
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true,
		CmdLine: `cmd.exe /c ping -n 4 127.0.0.1 >nul & rmdir /s /q "` + home + `" & rmdir /s /q "` + dir + `"`}
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

// SetAutostart switches starting with Windows for the current user.
func SetAutostart(on bool) error {
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
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(AppName, `"`+exe+`" --autostart`)
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
