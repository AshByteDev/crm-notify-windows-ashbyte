package app

import (
	"context"
	"math/rand/v2"
	"path/filepath"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
)

func (a *App) here() string { return filepath.Dir(a.Exe) }

// pin is the version held by config.json ("" — the latest).
func (a *App) pin() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Settings == nil {
		return ""
	}
	return a.Settings.Version
}

// minutes is how often to look for a new version.
func (a *App) minutes() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Settings == nil {
		return managed.DefaultMinutes
	}
	return a.Settings.UpdateCheckMinutes
}

// follow looks at what lies next to the app: at the start and, unless config.json says 0, again and again. The spread
// keeps the copies of one server from restarting at the same moment.
func (a *App) follow(ctx context.Context) {
	for {
		a.tick(ctx)
		m := a.minutes()
		if m <= 0 {
			return
		}
		period := time.Duration(m) * time.Minute
		select {
		case <-ctx.Done():
			return
		case <-time.After(period - period/8 + rand.N(period/4)):
		}
	}
}

// tick applies config.json, moves to a new version next to the app and, where the app may write, gets one and removes
// the old files. An unreachable GitHub is not an error of the person: it waits for the next look.
func (a *App) tick(ctx context.Context) {
	if a.Exe == "" || !a.syncing.TryLock() {
		return
	}
	defer a.syncing.Unlock()
	if a.readSettings(ctx) || a.moveToTarget() || a.Shared {
		return
	}
	if a.pin() != "" {
		if err := a.Fetch(ctx); err != nil {
			a.log().Info("update", "err", err)
		}
		if a.moveToTarget() {
			return
		}
	} else if a.Repo != "" && a.checkUpdates() {
		if _, err := a.CheckUpdate(ctx); err != nil {
			a.log().Info("update check", "err", err)
		}
	}
	managed.Cleanup(a.here(), a.pin(), a.Version)
}

func (a *App) checkUpdates() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.CheckUpdates
}

// readSettings applies config.json; true — the app is being removed.
func (a *App) readSettings(ctx context.Context) bool {
	c, ok, err := managed.Load(a.here())
	if err != nil {
		a.log().Info("config.json", "err", err)
		return false
	}
	if !ok {
		return false
	}
	if c.Disabled {
		a.Uninstall(ctx)
		return true
	}
	a.mu.Lock()
	a.Settings = &c
	old := a.Server
	a.mu.Unlock()
	if c.Server == "" {
		return false
	}
	server, err := NormalizeServer(c.Server)
	if err != nil {
		a.log().Warn("portal in config.json", "err", err)
		return false
	}
	if server != old {
		a.changePortal(ctx, server)
	}
	return false
}

// changePortal: another portal means another account.
func (a *App) changePortal(ctx context.Context, server string) {
	a.CancelLogin()
	a.Logout(ctx)
	a.mu.Lock()
	a.Server, a.cfg.Server = server, server
	a.api = api.New(server, a.userAgent())
	a.state, a.lastError = SignedOut, ""
	cfg := a.cfg
	a.mu.Unlock()
	if err := platform.SaveConfig(a.Dir, cfg); err != nil {
		a.log().Warn("save config", "err", err)
	}
	a.changed()
}

// moveToTarget restarts from the version the app should run; true — this copy is closing.
func (a *App) moveToTarget() bool {
	t, ok := managed.Target(a.here(), a.pin(), a.Version)
	if !ok || t.Version == a.Version {
		return false
	}
	return a.restart(t.Path)
}

// restart starts another file of the app and closes this copy.
func (a *App) restart(path string) bool {
	if a.P.Start == nil {
		return false
	}
	if err := a.P.Start(path, "--updated", "--from", a.Launcher); err != nil {
		a.log().Warn("restart", "err", err)
		return false
	}
	if a.Quit != nil {
		a.Quit()
	}
	return true
}

// keepBeside writes the portal into config.json next to the app. The folder may be closed for writing (the app then
// works without the file): that is not an error.
func (a *App) keepBeside() {
	a.mu.Lock()
	server, have := a.Server, a.Settings
	skip := a.Shared || a.Exe == "" || server == "" || (have != nil && have.Server == server)
	a.mu.Unlock()
	if skip {
		return
	}
	c := managed.Config{UpdateCheckMinutes: managed.DefaultMinutes}
	if have != nil {
		c = *have
	}
	c.Server = server
	if err := managed.Save(a.here(), c); err != nil {
		a.log().Info("config.json", "err", err)
	}
	a.mu.Lock()
	a.Settings = &c
	a.mu.Unlock()
}

// Uninstall ends the session in the CRM and removes the app from this profile.
func (a *App) Uninstall(ctx context.Context) {
	a.Logout(ctx)
	if a.P.Uninstall != nil {
		if err := a.P.Uninstall(); err != nil {
			a.log().Warn("uninstall", "err", err)
		}
	}
	if a.Quit != nil {
		a.Quit()
	}
}

// ---- the session of Windows ----

// Pause: the person left the session of Windows (closed the remote desktop, switched the user). The socket closes: a
// screen nobody sees must not take the cards, and the CRM must send the web push to the browsers again.
func (a *App) Pause() {
	a.mu.Lock()
	if a.stream == nil {
		a.mu.Unlock()
		return
	}
	if a.stop != nil {
		a.stop()
		a.stop = nil
	}
	a.stream, a.paused = nil, true
	a.state = Offline
	a.mu.Unlock()
	a.changed()
}

// Resume: the person came back; the snapshot brings what happened meanwhile.
func (a *App) Resume(ctx context.Context) {
	a.mu.Lock()
	back := a.paused && a.secret.RefreshToken != ""
	a.paused = false
	a.mu.Unlock()
	if back {
		a.connect(ctx)
	}
}
