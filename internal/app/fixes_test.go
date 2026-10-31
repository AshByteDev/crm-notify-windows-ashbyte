package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
)

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timeout: %s", what)
}

// readCRM gives one card in every snapshot and loses the connection on the first notify.read.
type readCRM struct {
	mu    sync.Mutex
	conns int
	reads int
	at    []time.Time // the time of the card in the snapshot of each connection
}

func (f *readCRM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	at := f.at[min(f.conns, len(f.at)-1)]
	f.conns++
	f.mu.Unlock()
	ctx := r.Context()
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var in struct{ ID, Type string }
		_ = json.Unmarshal(raw, &in)
		answer := map[string]any{"re": in.ID, "ok": true, "data": map[string]any{}}
		switch in.Type {
		case "notify.snapshot":
			answer["data"] = api.Snapshot{Cards: []api.Card{{Key: "chat-1", Count: 1, At: at}}, Total: 1}
		case "notify.read":
			f.mu.Lock()
			f.reads++
			first := f.reads == 1
			f.mu.Unlock()
			if first {
				_ = c.CloseNow()
				return
			}
		}
		b, _ := json.Marshal(answer)
		_ = c.Write(ctx, websocket.MessageText, b)
	}
}

func (f *readCRM) counts() (conns, reads int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, f.reads
}

func online(t *testing.T, ctx context.Context, url string) *App {
	t.Helper()
	a := &App{Dir: t.TempDir(), Version: "1"}
	a.Start(ctx)
	a.Server, a.api = url, api.New(url, "test")
	a.access, a.accessExp = "tok", time.Now().Add(time.Hour)
	a.connect(ctx)
	eventually(t, "the first snapshot", func() bool { _, ok := a.Q.Get("chat-1"); return ok })
	return a
}

// «Прочитано» without a connection reaches the CRM after the reconnect.
func TestReadIsSentAfterReconnect(t *testing.T) {
	t1 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	crm := &readCRM{at: []time.Time{t1}}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := online(t, ctx, srv.URL)
	a.Read(ctx, "chat-1")
	eventually(t, "the read sent again", func() bool { _, reads := crm.counts(); return reads == 2 })
	if _, ok := a.Q.Get("chat-1"); ok {
		t.Fatal("the card stays hidden")
	}
}

// A message that came meanwhile must not be read unseen: the card returns, the old «Прочитано» is dropped.
func TestReadIsDroppedWhenCardIsNewer(t *testing.T) {
	t1 := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	crm := &readCRM{at: []time.Time{t1, t1.Add(time.Minute)}}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := online(t, ctx, srv.URL)
	a.Read(ctx, "chat-1")
	eventually(t, "the card back", func() bool {
		c, ok := a.Q.Get("chat-1")
		return ok && c.At.After(t1)
	})
	time.Sleep(100 * time.Millisecond)
	if _, reads := crm.counts(); reads != 1 {
		t.Fatalf("reads %d", reads)
	}
	a.mu.Lock()
	left := len(a.unsent)
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("unsent %d", left)
	}
}

func TestCancelLogin(t *testing.T) {
	ctx := context.Background()
	a := &App{Dir: t.TempDir(), Version: "1", P: Platform{OpenURL: func(string) error { return nil }}}
	a.Start(ctx)
	a.Server, a.state = "http://127.0.0.1:1", SignedOut
	a.Login(ctx)
	if a.View().State != SigningIn {
		t.Fatal(a.View().State)
	}
	a.CancelLogin()
	eventually(t, "signed out", func() bool { return a.View().State == SignedOut })
	a.Login(ctx) // «Войти» works again at once
	if a.View().State != SigningIn {
		t.Fatal(a.View().State)
	}
	a.CancelLogin()
	eventually(t, "signed out", func() bool { return a.View().State == SignedOut })
}

// Changing the portal during a sign-in: the end of the waiting must not take the app back from the address window.
func TestChangeServerDuringLogin(t *testing.T) {
	ctx := context.Background()
	a := &App{Dir: t.TempDir(), Version: "1", P: Platform{OpenURL: func(string) error { return nil }}}
	a.Start(ctx)
	a.Server, a.state = "http://127.0.0.1:1", SignedOut
	a.Login(ctx)
	a.ChangeServer(ctx)
	time.Sleep(100 * time.Millisecond)
	if a.View().State != NeedServer {
		t.Fatal(a.View().State)
	}
}

// An expired key does not leave the session alive in the CRM.
func TestLogoutRefreshesExpiredKey(t *testing.T) {
	var mu sync.Mutex
	var loggedOut string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/desktop/refresh":
			fmt.Fprint(w, `{"access_token":"new","expires_at":"2099-01-01T00:00:00Z","refresh_token":"r2"}`)
		case "/api/v1/auth/logout":
			mu.Lock()
			loggedOut = r.Header.Get("Authorization")
			mu.Unlock()
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	a := &App{Dir: t.TempDir(), Version: "1"}
	a.Start(ctx)
	a.api = api.New(srv.URL, "test")
	a.access, a.accessExp, a.secret = "old", time.Now().Add(-time.Minute), platform.Secret{RefreshToken: "r1"}
	a.Logout(ctx)
	mu.Lock()
	defer mu.Unlock()
	if loggedOut != "Bearer new" {
		t.Fatalf("logout with %q", loggedOut)
	}
	if a.View().State != SignedOut {
		t.Fatal(a.View().State)
	}
}

// A broken .sha256 of a release is an error of the check, not a crash of the app.
func TestEmptyChecksumFile(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/releases/latest" {
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[
				{"name":"AshCRM-Notify.exe","browser_download_url":"%[1]s/exe"},
				{"name":"AshCRM-Notify.exe.sha256","browser_download_url":"%[1]s/sum"}]}`, srv.URL)
		}
	}))
	defer srv.Close()
	GitHubAPI, downloadPrefix = srv.URL, srv.URL+"/"
	a := &App{Version: "0.0.3", Repo: "o/r", Dir: t.TempDir()}
	a.Start(context.Background())
	if _, err := a.CheckUpdate(context.Background()); err == nil || !strings.Contains(err.Error(), "контрольной суммы") {
		t.Fatalf("got %v", err)
	}
}

func TestPosition(t *testing.T) {
	dir := t.TempDir()
	a := &App{Dir: dir}
	a.Start(context.Background())
	if p := a.View().Position; p != BottomRight {
		t.Fatalf("default %q", p)
	}
	a.SetPosition(TopCenter)
	b := &App{Dir: dir}
	b.Start(context.Background())
	if p := b.View().Position; p != TopCenter {
		t.Fatalf("after a restart %q", p)
	}
	b.SetPosition("somewhere")
	if p := b.View().Position; p != BottomRight {
		t.Fatalf("unknown place %q", p)
	}
}

// The check in the settings: the account and the CRM in the text, a local card with a sound, nothing sent to read.
func TestNotifyCheck(t *testing.T) {
	crm := &readCRM{at: []time.Time{time.Now()}}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := online(t, ctx, srv.URL)
	sounds := 0
	a.P.PlaySound = func(string) { sounds++ }
	a.mu.Lock()
	a.user = "Иван Петров"
	a.mu.Unlock()
	a.TestNotify(ctx)
	if v := a.View(); !strings.Contains(v.Test, "«Иван Петров»") || !strings.Contains(v.Test, "новых: 1") {
		t.Fatal(v.Test)
	}
	if _, ok := a.Q.Get(TestKey); !ok || sounds != 1 {
		t.Fatalf("card %v sounds %d", ok, sounds)
	}
	a.handlerSync(api.Snapshot{})
	if _, ok := a.Q.Get(TestKey); !ok {
		t.Fatal("the state of the server keeps a local card")
	}
	a.Read(ctx, TestKey)
	time.Sleep(50 * time.Millisecond)
	if _, reads := crm.counts(); reads != 0 {
		t.Fatalf("reads %d", reads)
	}
}

func (a *App) handlerSync(s api.Snapshot) { handler{a}.Sync(s) }

func TestNotifyCheckSignedOutAndDND(t *testing.T) {
	ctx := context.Background()
	a := &App{Dir: t.TempDir(), Version: "1"}
	a.Start(ctx)
	a.SetDND(true)
	a.TestNotify(ctx)
	v := a.View()
	if !strings.Contains(v.Test, "Вход не выполнен") || !strings.Contains(v.Test, "Не беспокоить") {
		t.Fatal(v.Test)
	}
	if v.Queue.Waiting != 1 {
		t.Fatalf("%+v", v.Queue)
	}
}

// allCRM counts notify.read and gives a card that came after the click in every snapshot.
type allCRM struct {
	mu    sync.Mutex
	reads []string
}

func (f *allCRM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := r.Context()
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var in struct {
			ID, Type string
			Data     struct{ Key string }
		}
		_ = json.Unmarshal(raw, &in)
		answer := map[string]any{"re": in.ID, "ok": true, "data": map[string]any{}}
		switch in.Type {
		case "notify.snapshot":
			answer["data"] = api.Snapshot{Cards: []api.Card{{Key: "chat-1", Count: 1, At: time.Now()}}, Total: 1}
		case "notify.read":
			f.mu.Lock()
			f.reads = append(f.reads, in.Data.Key)
			f.mu.Unlock()
		}
		b, _ := json.Marshal(answer)
		_ = c.Write(ctx, websocket.MessageText, b)
	}
}

func TestReadAll(t *testing.T) {
	crm := &allCRM{}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := online(t, ctx, srv.URL)
	old := time.Now().Add(-time.Hour)
	a.Q.Card(api.Card{Key: "chat-2", Count: 1, At: old})
	a.Q.Card(api.Card{Key: "chat-3", Count: 1, At: old})
	a.Q.Local(api.Card{Key: UpdateKey, At: old})
	a.ReadAll(ctx)
	eventually(t, "all read", func() bool {
		crm.mu.Lock()
		defer crm.mu.Unlock()
		return len(crm.reads) == 3
	})
	if v := a.View(); v.Queue.Unread != 0 {
		t.Fatalf("%+v", v.Queue)
	}
	if _, ok := a.Q.Get(UpdateKey); !ok {
		t.Fatal("the card of the app stays")
	}
}

// The rest of the server arrives with notify.sync; a message after the click stays unread.
func TestReadAllContinuesAfterSync(t *testing.T) {
	crm := &allCRM{}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := online(t, ctx, srv.URL)
	old := time.Now().Add(-time.Hour)
	a.handlerSync(api.Snapshot{Cards: []api.Card{{Key: "chat-1", Count: 1, At: old}}, Total: 2})
	a.ReadAll(ctx)
	a.handlerSync(api.Snapshot{Cards: []api.Card{{Key: "chat-2", Count: 1, At: old}, {Key: "chat-9", Count: 1, At: time.Now().Add(time.Hour)}}, Total: 2})
	eventually(t, "both read", func() bool {
		crm.mu.Lock()
		defer crm.mu.Unlock()
		return len(crm.reads) == 2
	})
	if _, ok := a.Q.Get("chat-9"); !ok {
		t.Fatal("a newer message is read blindly")
	}
}
