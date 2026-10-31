package app

import (
	"context"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
)

// The folder of the administrator is checked about once an hour; the spread keeps the copies of one server from reading
// it at the same moment.
func folderPause() time.Duration { return 50*time.Minute + rand.N(20*time.Minute) }
func firstPause() time.Duration  { return rand.N(10 * time.Minute) }

// followFolder keeps a managed copy in step with the folder of the administrator.
func (a *App) followFolder(ctx context.Context) {
	wait := firstPause()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		a.syncFolder(ctx)
		wait = folderPause()
	}
}

// syncFolder applies what the administrator has put in the folder: the removal, the settings, another version. An
// unreachable folder or a half-copied file is not an error of the person: it waits for the next check.
func (a *App) syncFolder(ctx context.Context) {
	if !a.syncing.TryLock() {
		return
	}
	defer a.syncing.Unlock()
	a.mu.Lock()
	cur := *a.Managed
	a.mu.Unlock()
	folder := cur.UpdateFolder
	c, ok, err := managed.Load(folder)
	if err != nil {
		a.log().Info("managed settings", "err", err)
		return
	}
	if ok {
		if c.Disabled {
			a.Uninstall(ctx)
			return
		}
		if c != cur {
			a.applyManaged(ctx, c)
		}
	}
	want, err := managed.ReadSum(folder)
	if err != nil {
		a.log().Info("managed update", "err", err)
		return
	}
	have, err := managed.Sum(a.Exe)
	if err != nil {
		a.log().Warn("managed update", "err", err)
		return
	}
	if strings.EqualFold(have, want) {
		return
	}
	if a.P.Replace == nil {
		return
	}
	next := filepath.Join(filepath.Dir(a.Exe), managed.NextName)
	if err := managed.Fetch(filepath.Join(folder, managed.ExeName), next, want); err != nil {
		a.log().Info("managed update", "err", err)
		return
	}
	if err := a.P.Replace(a.Exe, next); err != nil {
		_ = os.Remove(next)
		a.log().Warn("managed update", "err", err)
		return
	}
	if a.Quit != nil {
		a.Quit()
	}
}

// applyManaged keeps the new settings of the administrator; another portal means another account.
func (a *App) applyManaged(ctx context.Context, c managed.Config) {
	if err := managed.Save(filepath.Dir(a.Exe), c); err != nil {
		a.log().Warn("managed settings", "err", err)
		return
	}
	a.mu.Lock()
	*a.Managed = c
	old := a.Server
	a.mu.Unlock()
	if c.Server == "" {
		return
	}
	server, err := NormalizeServer(c.Server)
	if err != nil {
		a.log().Warn("managed portal", "err", err)
		return
	}
	if server == old {
		return
	}
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
