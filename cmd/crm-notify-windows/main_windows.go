//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/winui"
)

func main() {
	if slices.Contains(os.Args[1:], "--uninstall") {
		uninstall()
		return
	}
	if deployed() {
		return
	}
	// one copy per user; after an update the old copy is still closing — wait for it instead of «уже запущен»
	running := winui.AlreadyRunning()
	installed := slices.Contains(os.Args[1:], "--installed")
	if installed || slices.Contains(os.Args[1:], "--updated") {
		running = waitClosed(running)
	}
	if running {
		winui.Message("AshCRM Notify уже запущен — значок в области уведомлений (у часов).")
		return
	}
	if installed {
		settle()
	}
	a, err := newApp()
	if err != nil {
		winui.Message("Не удалось запустить AshCRM Notify: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &winui.UI{A: a, Ctx: ctx}
	a.Changed, a.Quit = u.Notify, u.Quit
	a.Start(ctx)
	if err := u.Run(); err != nil {
		winui.Message("AshCRM Notify: " + err.Error())
	}
}

// waitClosed gives the other copy ten seconds to close.
func waitClosed(running bool) bool {
	for i := 0; i < 50 && running; i++ {
		time.Sleep(200 * time.Millisecond)
		running = winui.AlreadyRunning()
	}
	return running
}

// deployed: with a folder in update_source of config.json next to it, the app puts itself and the settings into the profile of the
// user and starts from there; true — this process has nothing more to do. Otherwise it is an ordinary copy and runs
// where it is.
func deployed() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	home, err := platform.Home()
	if err != nil || managed.SameDir(filepath.Dir(exe), home) {
		return false
	}
	c, ok, err := managed.Load(filepath.Dir(exe))
	if err != nil {
		winui.Message("Не удалось прочитать настройки администратора: " + err.Error())
		return true
	}
	if !ok || !c.Managed() {
		return false
	}
	if c.Disabled {
		winui.Message("Администратор отключил AshCRM Notify.")
		return true
	}
	changed, err := managed.Install(exe, c, home)
	if err != nil {
		winui.Message("Не удалось установить AshCRM Notify: " + err.Error())
		return true
	}
	target := filepath.Join(home, managed.ExeName)
	if !changed {
		err = exec.Command(target).Start()
	} else {
		winui.CloseRunning()
		err = exec.Command(target, "--installed").Start()
	}
	if err != nil {
		winui.Message("Не удалось запустить AshCRM Notify: " + err.Error())
	}
	return true
}

// settle finishes the installation: autostart, the shortcut; the sign-in will not ask for autostart again.
func settle() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = platform.SetAutostart(true)
	_ = platform.Shortcut(exe)
	if dir, err := platform.Dir(); err == nil {
		cfg := platform.LoadConfig(dir)
		cfg.AutostartSet = true
		_ = platform.SaveConfig(dir, cfg)
	}
}

// uninstall closes the running copy, ends the session in the CRM and removes the app from this profile.
func uninstall() {
	winui.CloseRunning()
	if waitClosed(winui.AlreadyRunning()) {
		winui.Message("Не удалось закрыть AshCRM Notify.")
		return
	}
	a, err := newApp()
	if err != nil {
		winui.Message("Не удалось удалить AshCRM Notify: " + err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a.Load()
	winui.Message("AshCRM Notify будет удалён из этого профиля.") // before the removal: a waiting process holds its .exe
	a.Uninstall(ctx)
}
