//go:build !windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
)

// Development on Linux/macOS: the file itself is the protection (0600), no autostart, sounds as a terminal bell.

func protect(b []byte) ([]byte, error)   { return b, nil }
func unprotect(b []byte) ([]byte, error) { return b, nil }

// OpenURL opens a page in the default browser.
func OpenURL(u string) error {
	cmd := "xdg-open"
	if _, err := exec.LookPath(cmd); err != nil {
		cmd = "open"
	}
	return exec.Command(cmd, u).Start()
}

// Autostart reports whether the app starts with the system.
func Autostart() bool { return false }

// SetAutostart switches starting with the system.
func SetAutostart(bool) error { return nil }

// PlaySound plays a sound class.
func PlaySound(class string) { fmt.Fprint(os.Stderr, "\a") }

// Replace is not supported outside Windows (nil in app.Platform).
var Replace func(running, downloaded string) error

// CleanupOld does nothing outside Windows.
func CleanupOld() {}
