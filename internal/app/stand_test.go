//go:build stand

package app

// The whole client against a running AshCRM (the local stand): go test -tags stand ./internal/app/
// NOTIFY_SERVER=http://localhost:8780 NOTIFY_PASSWORD=<demo password> [NOTIFY_USER=demo-op1 NOTIFY_PEER=demo-op2]

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type web struct {
	t      *testing.T
	server string
	token  string
	id     string
}

func (w *web) do(method, path string, in any) map[string]any {
	w.t.Helper()
	var body *bytes.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, w.server+"/api/v1"+path, body)
	req.Header.Set("Content-Type", "application/json")
	if w.token != "" {
		req.Header.Set("Authorization", "Bearer "+w.token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode >= 300 {
		w.t.Fatalf("%s %s: %d %v", method, path, res.StatusCode, out)
	}
	return out
}

func signIn(t *testing.T, server, user, password string) *web {
	w := &web{t: t, server: server}
	r := w.do("POST", "/auth/login", map[string]any{"login": user, "password": password})
	w.token = r["access_token"].(string)
	w.id = r["user"].(map[string]any)["id"].(string)
	return w
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(15 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timeout: %s", what)
}

func TestAgainstStand(t *testing.T) {
	server, password := env("NOTIFY_SERVER", ""), env("NOTIFY_PASSWORD", "")
	if server == "" || password == "" {
		t.Skip("NOTIFY_SERVER and NOTIFY_PASSWORD are not set")
	}
	me := signIn(t, server, env("NOTIFY_USER", "demo-op1"), password)
	peer := signIn(t, server, env("NOTIFY_PEER", "demo-op2"), password)
	var sounds atomic.Int32
	a := &App{Version: "0.0.0-test", Dir: t.TempDir(), Device: "STAND-TEST", P: Platform{
		// the browser of the employee: the page «Подключить» → the code back to the loopback of the app
		OpenURL: func(raw string) error {
			u, _ := url.Parse(raw)
			q := u.Query()
			code := me.do("POST", "/auth/desktop/authorize", map[string]any{"challenge": q.Get("challenge"), "device": q.Get("device")})["code"].(string)
			go func() {
				res, err := http.Get("http://127.0.0.1:" + q.Get("port") + "/callback?code=" + code + "&state=" + q.Get("state"))
				if err == nil {
					res.Body.Close()
				}
			}()
			return nil
		},
		PlaySound: func(string) { sounds.Add(1) },
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	if a.View().State != NeedServer {
		t.Fatal("the first start asks the portal")
	}
	if err := a.SetServer(ctx, "localhost:1"); err == nil {
		t.Fatal("no portal there")
	}
	// the portal typed as a page of the CRM: cut to the portal, checked, then the sign-in starts by itself
	if err := a.SetServer(ctx, server+"/team"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "online", func() bool { return a.View().State == Online })
	waitFor(t, "the name", func() bool { return a.View().User != "" })
	if e := a.View().Error; e != "" {
		t.Fatalf("the CRM and the app must speak one contract: %s", e)
	}

	// a colleague writes → a card with sound
	dm := peer.do("POST", "/chats/direct", map[string]any{"user_id": me.id})["id"].(string)
	stamp := time.Now().Format("150405.000")
	peer.do("POST", "/chats/"+dm+"/messages", map[string]any{"body": "Проверка приложения " + stamp})
	key := "chat-" + dm
	waitFor(t, "the card", func() bool {
		c, ok := a.Q.Get(key)
		return ok && strings.Contains(c.Body, stamp)
	})
	if sounds.Load() == 0 {
		t.Error("a sound for the new card")
	}

	// «Прочитано» in the app reads the chat in the CRM
	a.Read(ctx, key)
	waitFor(t, "read in the CRM", func() bool {
		c := me.do("GET", "/chats/"+dm, nil)
		return c["my_last_read_seq"] == c["last_seq"]
	})

	// read in the browser → the card goes away (notify.sync)
	peer.do("POST", "/chats/"+dm+"/messages", map[string]any{"body": "Ещё одно " + stamp})
	waitFor(t, "the second card", func() bool { _, ok := a.Q.Get(key); return ok })
	c := me.do("GET", "/chats/"+dm, nil)
	me.do("POST", "/chats/"+dm+"/read", map[string]any{"seq": c["last_seq"]})
	waitFor(t, "cleared after reading in the browser", func() bool { _, ok := a.Q.Get(key); return !ok })

	// a restart keeps the session (the protected refresh token)
	cancel()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	b := &App{Version: "0.0.0-test", Dir: a.Dir, Device: "STAND-TEST", P: a.P}
	b.Start(ctx2)
	waitFor(t, "online after restart", func() bool { return b.View().State == Online })

	b.Logout(ctx2)
	if b.View().State != SignedOut {
		t.Fatal("signed out")
	}
}
