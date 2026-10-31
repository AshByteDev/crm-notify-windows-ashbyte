//go:build windows

package platform

import (
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A shortcut on the desktop through IShellLinkW and IPersistFile, without cgo.

var (
	ole32            = windows.NewLazySystemDLL("ole32.dll")
	coCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidShellLink = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLink   = windows.GUID{Data1: 0x000214F9, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile = windows.GUID{Data1: 0x0000010B, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// places in the tables of the interfaces
const (
	vtQueryInterface  = 0
	vtRelease         = 2
	vtSetIconLocation = 17
	vtSetPath         = 20
	vtSave            = 6
	clsctxInproc      = 1
)

// comObject is what a COM pointer points at: the table of its methods.
type comObject struct{ methods *[32]uintptr }

// call calls a method of a COM object; a negative HRESULT is an error.
func (o *comObject) call(method int, args ...uintptr) error {
	if r, _, _ := syscall.SyscallN(o.methods[method], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...); int32(r) < 0 {
		return syscall.Errno(r)
	}
	return nil
}

func shortcutPath() (string, error) {
	desktop, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(desktop, AppName+".lnk"), nil
}

// Shortcut puts a shortcut to the file on the desktop of the user.
func Shortcut(target string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	path, err := shortcutPath()
	if err != nil {
		return err
	}
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err != nil {
		return err
	}
	defer windows.CoUninitialize()
	var link *comObject
	if r, _, _ := coCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellLink)), 0, clsctxInproc,
		uintptr(unsafe.Pointer(&iidShellLink)), uintptr(unsafe.Pointer(&link))); int32(r) < 0 {
		return syscall.Errno(r)
	}
	defer link.call(vtRelease)
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err := link.call(vtSetPath, uintptr(unsafe.Pointer(file))); err != nil {
		return err
	}
	if err := link.call(vtSetIconLocation, uintptr(unsafe.Pointer(file)), 0); err != nil {
		return err
	}
	var persist *comObject
	if err := link.call(vtQueryInterface, uintptr(unsafe.Pointer(&iidPersistFile)), uintptr(unsafe.Pointer(&persist))); err != nil {
		return err
	}
	defer persist.call(vtRelease)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return persist.call(vtSave, uintptr(unsafe.Pointer(name)), 1)
}
