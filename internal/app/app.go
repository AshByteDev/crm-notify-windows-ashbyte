// Package app ties the parts together: the session (sign-in through the browser, the rotating refresh token), the
// stream of the CRM, the queue of cards and the actions of the screen. It knows nothing of windows: the UI reads View
// and calls the actions; Changed tells it to redraw.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/login"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/queue"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stream"
)

// States of the app (the tray icon and its tooltip).
const (
	SignedOut  = "signed_out"
	SigningIn  = "signing_in"
	Connecting = stream.StateConnecting
	Online     = stream.StateOnline
	Offline    = stream.StateOffline
)

// UpdateKey is the local card «Доступна новая версия».
const UpdateKey = "update"

// soundGap: a burst of messages gives one sound.
const soundGap = 2 * time.Second

// Platform is what the app needs from the system (platform package; fakes in tests).
type Platform struct {
	OpenURL      func(string) error
	PlaySound    func(class string)
	Autostart    func() bool
	SetAutostart func(bool) error
}

// App is the whole client.
type App struct {
	Server    string
	Version   string
	UpdateURL string // JSON {"version": "0.1.2", "url": "https://…"}; empty — no update checks
	Dir       string // settings and the protected session
	Device    string // the computer name shown in «Мои устройства»
	P         Platform
	Log       *slog.Logger
	// Changed asks the UI to redraw (called from any goroutine).
	Changed func()

	Q   *queue.Manager
	api *api.Client

	mu        sync.Mutex
	cfg       platform.Config
	state     string
	user      string
	lastError string
	secret    platform.Secret
	access    string
	accessExp time.Time
	stream    *stream.Stream
	stop      context.CancelFunc
	lastSound time.Time
	refreshMu sync.Mutex
}

// View is what the tray and the cards show.
type View struct {
	State        string
	User         string
	Error        string
	Queue        queue.View
	DND          bool
	CheckUpdates bool
	Autostart    bool
	Version      string
	Server       string
}

func (a *App) log() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (a *App) changed() {
	if a.Changed != nil {
		a.Changed()
	}
}

// Start loads the settings and connects when a session is saved.
func (a *App) Start(ctx context.Context) {
	a.cfg = platform.LoadConfig(a.Dir)
	if a.cfg.Server != "" {
		a.Server = a.cfg.Server
	}
	a.api = api.New(a.Server, "AshCRM-Notify/"+a.Version+" (Windows)")
	a.Q = queue.New()
	a.Q.SetDND(a.cfg.DND)
	a.state = SignedOut
	if s, ok := platform.LoadSecret(a.Dir); ok {
		a.secret = s
		a.connect(ctx)
	}
	go a.updates(ctx)
	a.changed()
}

// View returns the current state.
func (a *App) View() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := View{State: a.state, User: a.user, Error: a.lastError, DND: a.cfg.DND, CheckUpdates: a.cfg.CheckUpdates,
		Version: a.Version, Server: a.Server}
	if a.Q != nil {
		v.Queue = a.Q.View()
	}
	if a.P.Autostart != nil {
		v.Autostart = a.P.Autostart()
	}
	return v
}

func (a *App) setState(s, errText string) {
	a.mu.Lock()
	a.state, a.lastError = s, errText
	a.mu.Unlock()
	a.changed()
}

// Login runs the browser sign-in (in the background; the state shows the progress).
func (a *App) Login(ctx context.Context) {
	a.mu.Lock()
	if a.state != SignedOut {
		a.mu.Unlock()
		return
	}
	a.state, a.lastError = SigningIn, ""
	a.mu.Unlock()
	a.changed()
	go func() {
		r, err := login.Run(ctx, a.Server, a.Device, a.P.OpenURL)
		if err != nil {
			a.setState(SignedOut, err.Error())
			return
		}
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		t, err := a.api.Token(tctx, r.Code, r.Verifier)
		if err != nil {
			a.setState(SignedOut, "не удалось подключить: "+err.Error())
			return
		}
		if err := a.keep(t); err != nil {
			a.setState(SignedOut, "не удалось сохранить вход: "+err.Error())
			return
		}
		a.mu.Lock()
		first := !a.cfg.AutostartSet
		a.cfg.AutostartSet = true
		cfg := a.cfg
		a.mu.Unlock()
		if first && a.P.SetAutostart != nil {
			_ = a.P.SetAutostart(true) // a notification app is useful only when running; the menu turns it off
		}
		_ = platform.SaveConfig(a.Dir, cfg)
		a.connect(ctx)
	}()
}

// keep saves a new pair of tokens (the refresh token is spent on use: it is written before anything else).
func (a *App) keep(t api.Tokens) error {
	s := platform.Secret{RefreshToken: t.RefreshToken, UserID: t.UserID, SessionID: t.SessionID}
	if err := platform.SaveSecret(a.Dir, s); err != nil {
		return err
	}
	a.mu.Lock()
	a.secret, a.access, a.accessExp = s, t.AccessToken, t.ExpiresAt
	a.mu.Unlock()
	return nil
}

// token is stream.Tokens: a valid access token, refreshed when it ends within two minutes or when forced.
func (a *App) token(ctx context.Context, force bool) (string, error) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	a.mu.Lock()
	access, exp, refresh := a.access, a.accessExp, a.secret.RefreshToken
	a.mu.Unlock()
	if !force && access != "" && time.Until(exp) > 2*time.Minute {
		return access, nil
	}
	if refresh == "" {
		return "", stream.ErrSignedOut
	}
	t, err := a.api.Refresh(ctx, refresh)
	if err != nil {
		return "", err
	}
	if err := a.keep(t); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// connect starts the stream (and reads the name of the employee for the tray).
func (a *App) connect(ctx context.Context) {
	a.mu.Lock()
	if a.stop != nil {
		a.stop()
	}
	sctx, stop := context.WithCancel(ctx)
	a.stop = stop
	s := &stream.Stream{URL: stream.WSURL(a.Server), Tokens: a.token, H: handler{a}, Log: a.log()}
	a.stream = s
	a.state = Connecting
	a.mu.Unlock()
	a.changed()
	go func() {
		if tok, err := a.token(sctx, false); err == nil {
			if me, err := a.api.Me(sctx, tok); err == nil {
				a.mu.Lock()
				a.user = me.DisplayName
				a.mu.Unlock()
				a.changed()
			}
		}
	}()
	go func() {
		err := s.Run(sctx)
		if errors.Is(err, stream.ErrSignedOut) {
			a.signedOut("сессия завершена — войдите снова")
		}
	}()
}

func (a *App) signedOut(why string) {
	a.mu.Lock()
	if a.stop != nil {
		a.stop()
		a.stop = nil
	}
	a.stream, a.secret, a.access, a.user = nil, platform.Secret{}, "", ""
	a.state, a.lastError = SignedOut, why
	a.mu.Unlock()
	platform.DeleteSecret(a.Dir)
	a.Q.Reset()
	a.changed()
}

// Logout ends the session in the CRM and here.
func (a *App) Logout(ctx context.Context) {
	a.mu.Lock()
	access := a.access
	a.mu.Unlock()
	if access != "" {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_ = a.api.Logout(lctx, access)
		cancel()
	}
	a.signedOut("")
}

// Reconnect: the computer woke up or the network came back.
func (a *App) Reconnect() {
	a.mu.Lock()
	s := a.stream
	a.mu.Unlock()
	if s != nil {
		s.Reconnect()
	}
}

// ---- actions of the cards ----

func (a *App) open(path string) {
	u := path
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = strings.TrimRight(a.Server, "/") + path
	}
	if err := a.P.OpenURL(u); err != nil {
		a.log().Warn("open url", "err", err)
	}
}

// Open opens the place of the card in the CRM; the card leaves the screen (reading there clears it everywhere).
func (a *App) Open(key string) {
	if c, ok := a.Q.Get(key); ok {
		a.open(c.URL)
	}
	a.Q.Dismiss(key)
	a.changed()
}

// Read is «Прочитано»: the card goes at once, the CRM reads the chat (every device drops it).
func (a *App) Read(ctx context.Context, key string) {
	a.Q.Dismiss(key)
	a.changed()
	if key == UpdateKey {
		return
	}
	a.mu.Lock()
	s := a.stream
	a.mu.Unlock()
	if s == nil {
		return
	}
	go func() {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := s.Read(rctx, key); err != nil {
			a.log().Warn("read", "key", key, "err", err)
		}
	}()
}

// Close (×) removes the card here only; a newer message brings it back.
func (a *App) Close(key string) {
	a.Q.Dismiss(key)
	a.changed()
}

// OpenCRM opens the CRM.
func (a *App) OpenCRM() { a.open("/team") }

// SetDND switches «Не беспокоить».
func (a *App) SetDND(on bool) {
	a.mu.Lock()
	a.cfg.DND = on
	cfg := a.cfg
	a.mu.Unlock()
	a.Q.SetDND(on)
	_ = platform.SaveConfig(a.Dir, cfg)
	a.changed()
}

// SetCheckUpdates switches the daily update check.
func (a *App) SetCheckUpdates(on bool) {
	a.mu.Lock()
	a.cfg.CheckUpdates = on
	cfg := a.cfg
	a.mu.Unlock()
	_ = platform.SaveConfig(a.Dir, cfg)
	a.changed()
}

// SetAutostart switches starting with Windows.
func (a *App) SetAutostart(on bool) {
	if a.P.SetAutostart != nil {
		if err := a.P.SetAutostart(on); err != nil {
			a.log().Warn("autostart", "err", err)
		}
	}
	a.changed()
}

func (a *App) play(class string) {
	if class == "" || a.P.PlaySound == nil {
		return
	}
	a.mu.Lock()
	if time.Since(a.lastSound) < soundGap {
		a.mu.Unlock()
		return
	}
	a.lastSound = time.Now()
	a.mu.Unlock()
	a.P.PlaySound(class)
}

// handler is the stream.Handler of the app.
type handler struct{ a *App }

func (h handler) Sync(s api.Snapshot) { h.a.play(h.a.Q.Sync(s)); h.a.changed() }
func (h handler) Card(c api.Card)     { h.a.play(h.a.Q.Card(c)); h.a.changed() }
func (h handler) Clear(keys []string) { h.a.Q.Clear(keys...); h.a.changed() }
func (h handler) State(s string) {
	if s == stream.StateSignedOut {
		return // signedOut() is called when Run returns
	}
	h.a.setState(s, "")
}

// ---- updates ----

type release struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	Notes   string `json:"notes"`
}

// newer compares versions "X.Y.Z".
func newer(v, than string) bool {
	parse := func(s string) [3]int {
		var out [3]int
		for i, p := range strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3) {
			out[i], _ = strconv.Atoi(p)
		}
		return out
	}
	a, b := parse(v), parse(than)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// updates checks the new version once a day (switched off in the menu or without UpdateURL) and shows a card.
func (a *App) updates(ctx context.Context) {
	if a.UpdateURL == "" {
		return
	}
	for {
		a.mu.Lock()
		on := a.cfg.CheckUpdates
		a.mu.Unlock()
		if on {
			if r, err := a.checkUpdate(ctx); err == nil && newer(r.Version, a.Version) {
				a.Q.Card(api.Card{Key: UpdateKey, Kind: "update", Title: "Доступна версия " + r.Version, Body: r.Notes,
					URL: r.URL, At: time.Now(), Sound: api.SoundOther})
				a.changed()
			} else if err != nil {
				a.log().Info("update check", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

func (a *App) checkUpdate(ctx context.Context) (release, error) {
	var r release
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", a.UpdateURL, nil)
	if err != nil {
		return r, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return r, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return r, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return r, json.NewDecoder(res.Body).Decode(&r)
}

// DeviceName is the name of the computer for «Мои устройства».
func DeviceName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "компьютер"
	}
	return h
}
