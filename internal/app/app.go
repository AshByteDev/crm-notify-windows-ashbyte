// Package app ties the parts together: the session (sign-in through the browser, the rotating refresh token), the
// stream of the CRM, the queue of cards and the actions of the screen. It knows nothing of windows: the UI reads View
// and calls the actions; Changed tells it to redraw.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/login"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/queue"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stream"
)

// States of the app (the tray icon and its tooltip).
const (
	NeedServer = "need_server" // the first start: the address of the portal is asked
	SignedOut  = "signed_out"
	SigningIn  = "signing_in"
	Connecting = stream.StateConnecting
	Online     = stream.StateOnline
	Offline    = stream.StateOffline
)

// activeWithin: the person counts as working while the last input is this recent (contract §5.7).
const activeWithin = time.Minute

// Contract is the version of docs/NOTIFY_CONTRACT.md the app speaks; a CRM with another major version needs another app.
const Contract = "1.2"

// Places of the cards on the screen (the setting «Где показывать уведомления»).
const (
	TopLeft      = "top-left"
	TopCenter    = "top-center"
	TopRight     = "top-right"
	BottomLeft   = "bottom-left"
	BottomCenter = "bottom-center"
	BottomRight  = "bottom-right"
)

// NormalPosition returns a known place of the cards; anything else (an old or edited config) is the default corner.
func NormalPosition(p string) string {
	switch p {
	case TopLeft, TopCenter, TopRight, BottomLeft, BottomCenter:
		return p
	}
	return BottomRight
}

// UpdateKey is the local card «Доступна новая версия».
const UpdateKey = "update"

// TestKey is the local card of the check in the settings.
const TestKey = "test"

// soundGap: a burst of messages gives one sound.
const soundGap = 2 * time.Second

// Platform is what the app needs from the system (platform package; fakes in tests).
type Platform struct {
	OpenURL      func(string) error
	PlaySound    func(class string)
	Autostart    func() bool
	SetAutostart func(bool) error
	// Start starts another file of the app (nil — not supported here).
	Start func(path string, args ...string) error
	// Uninstall removes the app from the profile of the user (nil — not supported here).
	Uninstall func() error
	// Idle is the time since the last input in this session of Windows; false — unknown (nil — not supported here).
	Idle func() (time.Duration, bool)
}

// App is the whole client.
type App struct {
	// Server is the portal of the CRM: asked on the first start and kept in config.json (DefaultServer prefills it).
	Server        string
	DefaultServer string
	Version       string
	// Repo of the releases on GitHub ("owner/name"); empty — no update checks.
	Repo   string
	Dir    string // settings and the protected session
	Device string // the computer name shown in «Мои устройства»
	P      Platform
	Exe    string // the running file
	// Launcher is the file with the constant name: autostart and the shortcut point at it.
	Launcher string
	// Shared: the app may not write next to itself (a terminal server): the administrator keeps config.json, new
	// versions come from the scheduled task.
	Shared bool
	// Settings is config.json next to the running file (nil — none yet).
	Settings *managed.Config
	Log      *slog.Logger
	// Changed asks the UI to redraw (called from any goroutine).
	Changed func()
	// Quit closes the app (after an update the new copy starts).
	Quit func()

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
	syncing   sync.Mutex // one look at the folder of the administrator at a time
	paused    bool       // the person left the session of Windows
	// «Прочитать все»: how many are still to read and up to what moment.
	readAllLeft int
	readAllAt   time.Time
	update      *Release
	updating    bool
	testing     string // the result of the last check in the settings
	// cancelLogin stops waiting for the browser.
	cancelLogin context.CancelFunc
	// unsent: cards marked «Прочитано» when the CRM could not be reached; sent again after the next snapshot.
	unsent map[string]bool
}

// View is what the tray and the cards show.
type View struct {
	State         string
	DefaultServer string
	Update        *Release // a newer version on GitHub
	Shared        bool     // the administrator decides on the updates
	FixedServer   bool     // the administrator has set the portal
	Updating      bool
	User          string
	Error         string
	Queue         queue.View
	DND           bool
	CheckUpdates  bool
	Autostart     bool
	Position      string // where the cards are shown: TopLeft … BottomRight
	Test          string // the result of the last check in the settings
	Version       string
	Server        string
}

func (a *App) userAgent() string { return "AshCRM-Notify/" + a.Version + " (Windows)" }

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
	a.Load()
	a.keepBeside()
	if a.secret.RefreshToken != "" {
		a.connect(ctx)
	}
	go a.follow(ctx)
	a.changed()
}

// Load reads the settings and the saved session, without connecting.
func (a *App) Load() {
	a.cfg = platform.LoadConfig(a.Dir)
	a.adoptManagedServer()
	a.Server = a.cfg.Server
	a.Q = queue.New()
	a.Q.SetDND(a.cfg.DND)
	a.applyShow()
	a.state = NeedServer
	if a.Server != "" {
		a.api = api.New(a.Server, a.userAgent())
		a.state = SignedOut
		if s, ok := platform.LoadSecret(a.Dir); ok {
			a.secret = s
		}
	}
}

// adoptManagedServer: the portal of config.json wins; the session of another portal is of no use.
func (a *App) adoptManagedServer() {
	if a.Settings == nil || a.Settings.Server == "" {
		return
	}
	s, err := NormalizeServer(a.Settings.Server)
	if err != nil {
		a.log().Warn("managed portal", "err", err)
		return
	}
	if s != a.cfg.Server {
		a.cfg.Server = s
		platform.DeleteSecret(a.Dir)
		if err := platform.SaveConfig(a.Dir, a.cfg); err != nil {
			a.log().Warn("save config", "err", err)
		}
	}
}

// View returns the current state.
func (a *App) View() View {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := View{State: a.state, User: a.user, Error: a.lastError, DND: a.cfg.DND, CheckUpdates: a.cfg.CheckUpdates,
		Version: a.Version, Server: a.Server, DefaultServer: a.DefaultServer, Update: a.update, Updating: a.updating,
		Position: NormalPosition(a.cfg.Position), Test: a.testing, Shared: a.Shared,
		FixedServer: a.Shared && a.Settings != nil && a.Settings.Server != ""}
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
	lctx, cancel := context.WithCancel(ctx)
	a.cancelLogin = cancel
	a.mu.Unlock()
	a.changed()
	go func() {
		defer cancel()
		r, err := login.Run(lctx, a.Server, a.Device, a.P.OpenURL)
		if err != nil {
			a.loginFailed(err.Error())
			return
		}
		tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		t, err := a.api.Token(tctx, r.Code, r.Verifier)
		if err != nil {
			a.loginFailed("не удалось подключить: " + err.Error())
			return
		}
		if err := a.keep(t); err != nil {
			a.loginFailed("не удалось сохранить вход: " + err.Error())
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

// loginFailed ends a sign-in, unless the state has already moved on (the portal is being changed).
func (a *App) loginFailed(why string) {
	a.mu.Lock()
	if a.state != SigningIn {
		a.mu.Unlock()
		return
	}
	a.state, a.lastError = SignedOut, why
	a.mu.Unlock()
	a.changed()
}

// CancelLogin stops waiting for the browser (the tab was closed): «Войти» works again at once.
func (a *App) CancelLogin() {
	a.mu.Lock()
	cancel := a.cancelLogin
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
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
	a.stop, a.paused = stop, false
	s := &stream.Stream{URL: stream.WSURL(a.Server), UserAgent: a.userAgent(), Tokens: a.token, H: handler{a}, Log: a.log()}
	if a.P.Idle != nil {
		s.Active = a.active
	}
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

// active is what the CRM learns about the person: only yes or no, never what they work in (contract §5.7).
func (a *App) active() (active, known bool) {
	idle, ok := a.P.Idle()
	return ok && idle < activeWithin, ok
}

func (a *App) signedOut(why string) {
	a.mu.Lock()
	if a.stop != nil {
		a.stop()
		a.stop = nil
	}
	a.stream, a.secret, a.access, a.user, a.unsent = nil, platform.Secret{}, "", "", nil
	a.state, a.lastError = SignedOut, why
	a.mu.Unlock()
	platform.DeleteSecret(a.Dir)
	a.Q.Reset()
	a.changed()
}

// Logout ends the session in the CRM and here.
func (a *App) Logout(ctx context.Context) {
	// through token(): the saved key may have expired, and the session in the CRM must end anyway
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if access, err := a.token(lctx, false); err == nil {
		_ = a.api.Logout(lctx, access)
	}
	cancel()
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

// open opens a page of the portal: the server gives paths only ("/team/…"), so a card can never send the browser to
// another site (a compromised or fake server included).
func (a *App) open(path string) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\\r\n") {
		path = "/team"
	}
	if err := a.P.OpenURL(strings.TrimRight(a.Server, "/") + path); err != nil {
		a.log().Warn("open url", "err", err)
	}
}

// openRelease opens the page of a release of the repository of the app.
func (a *App) openRelease(page string) {
	if !strings.HasPrefix(page, "https://github.com/"+a.Repo+"/releases/") {
		page = "https://github.com/" + a.Repo + "/releases/latest"
	}
	if err := a.P.OpenURL(page); err != nil {
		a.log().Warn("open url", "err", err)
	}
}

// Open opens the place of the card in the CRM; the card leaves the screen (reading there clears it everywhere).
func (a *App) Open(key string) {
	if c, ok := a.Q.Get(key); ok {
		if key == UpdateKey {
			a.openRelease(c.URL)
		} else {
			a.open(c.URL)
		}
	}
	a.Q.Dismiss(key)
	a.changed()
}

// Read is «Прочитано»: the card goes at once, the CRM reads the chat (every device drops it).
func (a *App) Read(ctx context.Context, key string) {
	a.Q.Dismiss(key)
	a.changed()
	if key == UpdateKey || key == TestKey {
		return
	}
	a.mu.Lock()
	s := a.stream
	a.mu.Unlock()
	if s == nil {
		return
	}
	go a.sendRead(ctx, s, key)
}

// sendRead tells the CRM; when it cannot be reached, the key waits for the next snapshot.
func (a *App) sendRead(ctx context.Context, s *stream.Stream, key string) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := s.Read(ctx, key)
	if err == nil {
		return
	}
	a.log().Warn("read", "key", key, "err", err)
	if errors.Is(err, stream.ErrRefused) {
		return // read elsewhere or no longer there
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stream == s {
		if a.unsent == nil {
			a.unsent = map[string]bool{}
		}
		a.unsent[key] = true
	}
}

// sendUnsent repeats the «Прочитано» the CRM did not get. A card that came back holds a message the person has not
// seen: notify.read would read it too, so its old «Прочитано» is dropped.
func (a *App) sendUnsent() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key := range a.unsent {
		delete(a.unsent, key)
		if _, back := a.Q.Get(key); !back && a.stream != nil {
			go a.sendRead(context.Background(), a.stream, key)
		}
	}
}

// ReadAll reads everything unread at this moment. The app holds at most a snapshot of the cards, the rest comes with
// the next notify.sync: only what was there when the person asked is read, a newer message stays.
func (a *App) ReadAll(ctx context.Context) {
	left := a.Q.View().Unread
	a.mu.Lock()
	a.readAllLeft, a.readAllAt = left, time.Now()
	a.mu.Unlock()
	a.readNext(ctx)
}

// readNext reads the cards the app has now from those asked for.
func (a *App) readNext(ctx context.Context) {
	a.mu.Lock()
	s, left, at := a.stream, a.readAllLeft, a.readAllAt
	a.mu.Unlock()
	if s == nil || left <= 0 {
		return
	}
	keys := a.Q.Unread(at)
	if len(keys) > left {
		keys = keys[:left]
	}
	for _, k := range keys {
		a.Q.Dismiss(k)
		go a.sendRead(ctx, s, k)
	}
	a.mu.Lock()
	a.readAllLeft = left - len(keys)
	if len(keys) == 0 {
		a.readAllLeft = 0
	}
	a.mu.Unlock()
	a.changed()
}

// Close (×) removes the card here only; a newer message brings it back.
func (a *App) Close(key string) {
	a.Q.Dismiss(key)
	a.changed()
}

// TestNotify is the check in the settings: it asks the CRM through the socket under the signed-in account, then shows
// a test card with a sound. The CRM is not changed. Blocks while asking (call it in the background).
func (a *App) TestNotify(ctx context.Context) {
	a.mu.Lock()
	s, user, dnd := a.stream, a.user, a.cfg.DND
	a.testing = "Проверяю…"
	a.mu.Unlock()
	a.changed()
	var text string
	if s == nil {
		text = "Вход не выполнен: уведомления не придут, пока вы не войдёте."
	} else {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		sn, err := s.Check(cctx)
		cancel()
		switch {
		case err != nil:
			text = "Нет связи с CRM: " + err.Error()
		case user == "":
			text = fmt.Sprintf("Связь с CRM есть, новых уведомлений: %d.", sn.Total)
		default:
			text = fmt.Sprintf("Связь с CRM есть. Уведомления приходят сотруднику «%s», новых: %d.", user, sn.Total)
		}
	}
	if dnd {
		text += " Включено «Не беспокоить»: карточка появится после выключения."
	}
	a.Q.Local(api.Card{Key: TestKey, Kind: queue.KindTest, Title: "Проверка уведомлений", Body: text, URL: "/team", At: time.Now(), Sound: api.SoundOther})
	if a.P.PlaySound != nil {
		a.P.PlaySound(api.SoundOther)
	}
	a.mu.Lock()
	a.testing = text
	a.mu.Unlock()
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

// SetPosition chooses where the cards are shown.
func (a *App) SetPosition(p string) {
	a.mu.Lock()
	a.cfg.Position = NormalPosition(p)
	cfg := a.cfg
	a.mu.Unlock()
	if err := platform.SaveConfig(a.Dir, cfg); err != nil {
		a.log().Warn("save config", "err", err)
	}
	a.changed()
}

// SetCheckUpdates switches the regular update check.
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

func (h handler) Sync(s api.Snapshot) {
	h.a.Q.Sync(s)
	h.a.sendUnsent()
	h.a.readNext(context.Background())
	h.a.changed()
}
func (h handler) Card(c api.Card)     { h.a.play(h.a.Q.Card(c)); h.a.changed() }
func (h handler) Clear(keys []string) { h.a.Q.Clear(keys...); h.a.changed() }
func (h handler) Contract(v string) {
	if v == "" {
		v = "1.0" // CRM 0.0.40 before the field: contract 1.0
	}
	if major(v) != major(Contract) {
		h.a.mu.Lock()
		h.a.lastError = "портал работает по контракту " + v + ", приложение — " + Contract + ": обновите приложение"
		h.a.mu.Unlock()
		h.a.log().Warn("contract mismatch", "crm", v, "app", Contract)
		go func() { _, _ = h.a.CheckUpdate(context.Background()) }()
		h.a.changed()
	}
}

func major(v string) string { m, _, _ := strings.Cut(v, "."); return m }

func (h handler) State(s string) {
	if s == stream.StateSignedOut {
		return // signedOut() is called when Run returns
	}
	h.a.setState(s, "")
}

// DeviceName names the computer and the user for «Мои устройства»: on a terminal server many people share one name of
// the computer.
func DeviceName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		h = "компьютер"
	}
	if u := os.Getenv("USERNAME"); u != "" {
		return h + " / " + u
	}
	return h
}
