//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/winui"
)

func main() {
	if launched() {
		return
	}
	if slices.Contains(os.Args[1:], "--fetch-update") {
		fetchUpdate()
		return
	}
	if slices.Contains(os.Args[1:], "--uninstall") {
		uninstall()
		return
	}
	// one copy per user; after an update the old copy is still closing — wait for it instead of «уже запущен»
	running := winui.AlreadyRunning()
	if slices.Contains(os.Args[1:], "--updated") {
		running = waitClosed(running)
	}
	if running {
		winui.Message("AshCRM Notify уже запущен — значок в области уведомлений (у часов).")
		return
	}
	a, err := newApp()
	if err != nil {
		winui.Message("Не удалось запустить AshCRM Notify: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	settle(a)
	u := &winui.UI{A: a, Ctx: ctx}
	a.Changed, a.Quit = u.Notify, u.Quit
	a.Start(ctx)
	if err := u.Run(); err != nil {
		winui.Message("AshCRM Notify: " + err.Error())
	}
}

// launched: this is the file with the constant name and a version lies next to it: the version takes over and this
// process ends, so the file is never held. Without such a file the app runs from here.
func launched() bool {
	exe, err := os.Executable()
	if err != nil || version == "dev" || managed.IsVersion(exe) {
		return false
	}
	c, _, _ := managed.Load(filepath.Dir(exe))
	t, ok := managed.Target(filepath.Dir(exe), c.Version, version)
	if !ok {
		return false
	}
	return platform.Start(t.Path, append(os.Args[1:], "--from", exe)...) == nil
}

// settle finishes the first start of a user of a shared folder, where nobody installs anything: autostart and the
// shortcut. An ordinary copy gets autostart with the first sign-in.
func settle(a *app.App) {
	if !a.Shared || (a.Settings != nil && a.Settings.Disabled) {
		return
	}
	cfg := platform.LoadConfig(a.Dir)
	if cfg.AutostartSet {
		return
	}
	_ = platform.SetAutostart(a.Launcher, true)
	_ = platform.Shortcut(a.Launcher)
	cfg.AutostartSet = true
	_ = platform.SaveConfig(a.Dir, cfg)
}

// fetchUpdate is the scheduled task of a terminal server: it brings a new version next to the app and ends.
func fetchUpdate() {
	a, err := newApp()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	a.Load()
	if err := a.Fetch(ctx); err != nil {
		a.Log.Warn("fetch update", "err", err)
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
