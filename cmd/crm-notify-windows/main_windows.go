//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stickers"
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
	a.StickerDir = stickers.Prepare(stickers.SharedDir())
	u := &winui.UI{A: a, Ctx: ctx}
	a.Changed, a.Quit = u.Notify, u.Quit
	a.Start(ctx)
	if err := u.Run(); err != nil {
		winui.Message("AshCRM Notify: " + err.Error())
	}
}

// launched hands the start over to the file the app should run from, so the file with the constant name is never held:
// the launcher starts the newest version, and the file of a version moves into AshCRM-Notify-app.exe (managed.RunName),
// whose path Windows remembers for the icon. The copy already in the right file runs from here.
func launched() bool {
	exe, err := os.Executable()
	if err != nil || version == "dev" || managed.IsRun(exe) {
		return false
	}
	dir := filepath.Dir(exe)
	c, _, _ := managed.Load(dir)
	t, ok := managed.Target(dir, c.Version, version)
	if !ok {
		return false
	}
	path := managed.Prepare(dir, t, true) // in Program Files a person cannot write: the version file runs then
	if strings.EqualFold(filepath.Clean(path), filepath.Clean(exe)) {
		return false
	}
	args := os.Args[1:]
	if !managed.IsVersion(exe) { // a version got --from from the launcher already
		args = append(args, "--from", exe)
	}
	return platform.Start(path, args...) == nil
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
	// the folder of the stickers is created by SYSTEM, so no person owns it; the people add files there
	stickers.Prepare(stickers.SharedDir())
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
