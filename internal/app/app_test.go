package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		v, than string
		want    bool
	}{{"0.1.0", "0.0.9", true}, {"0.0.10", "0.0.9", true}, {"v1.0.0", "0.9.9", true}, {"0.0.3", "0.0.3", false}, {"0.0.2", "0.0.3", false}} {
		if newer(c.v, c.than) != c.want {
			t.Errorf("%s > %s", c.v, c.than)
		}
	}
}

func TestNormalizeServer(t *testing.T) {
	for in, want := range map[string]string{
		"crm.example.ru":                    "https://crm.example.ru",
		" https://crm.example.ru/team/abc ": "https://crm.example.ru",
		"http://10.0.0.5:8780/login":        "http://10.0.0.5:8780",
	} {
		if got, err := NormalizeServer(in); err != nil || got != want {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "ftp://x.ru", "https://"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProbe(t *testing.T) {
	var agent string
	crm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/desktop/token" {
			agent = r.Header.Get("User-Agent")
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"code":"unauthorized","message":"x"}}`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
	}))
	defer crm.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>") }))
	defer site.Close()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
	}))
	defer old.Close()
	ctx := context.Background()
	if err := probe(ctx, crm.URL, "AshCRM-Notify/1 (Windows)"); err != nil {
		t.Fatal(err)
	}
	if agent != "AshCRM-Notify/1 (Windows)" {
		t.Fatalf("User-Agent %q", agent)
	}
	if err := probe(ctx, site.URL, ""); err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("a site: %v", err)
	}
	if err := probe(ctx, old.URL, ""); err == nil || !strings.Contains(err.Error(), "старая версия") {
		t.Fatalf("an old CRM: %v", err)
	}
}

// A release on GitHub: found, downloaded next to the running file, checked by SHA-256, handed to Replace.
func TestUpdateFromRelease(t *testing.T) {
	exe := []byte("MZ new version")
	sum := sha256.Sum256(exe)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","body":"Что нового","html_url":"%s/page","assets":[
				{"name":"AshCRM-Notify.exe","browser_download_url":"%s/exe","digest":"sha256:%s"}]}`, srv.URL, srv.URL, hex.EncodeToString(sum[:]))
		case "/exe":
			_, _ = w.Write(exe)
		}
	}))
	defer srv.Close()
	GitHubAPI = srv.URL
	downloadPrefix = srv.URL + "/"
	dir := t.TempDir()
	var started string
	quit := false
	a := &App{Version: "0.0.3", Repo: "o/r", Dir: t.TempDir(), Exe: filepath.Join(dir, managed.ExeName), Quit: func() { quit = true },
		P: Platform{Start: func(path string, args ...string) error { started = path; return nil }}}
	a.Start(context.Background())
	r, err := a.CheckUpdate(context.Background())
	if err != nil || r == nil || r.Version != "9.9.9" {
		t.Fatalf("%+v %v", r, err)
	}
	if c, ok := a.Q.Get(UpdateKey); !ok || c.Title != "Доступна версия 9.9.9" || c.Body != "Что нового" {
		t.Fatalf("card %+v", c)
	}

	// a file that does not match its checksum is not kept
	good := a.update.SHA256
	a.mu.Lock()
	a.update.SHA256 = strings.Repeat("0", 64)
	a.mu.Unlock()
	if err := a.Update(context.Background()); err == nil || started != "" || quit {
		t.Fatalf("bad checksum: %v %q %v", err, started, quit)
	}
	if len(managed.List(dir)) != 0 {
		t.Fatal("a wrong file stays")
	}

	a.mu.Lock()
	a.update.SHA256 = good
	a.mu.Unlock()
	if err := a.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, managed.FileName("9.9.9"))
	if started != want || !quit {
		t.Fatalf("started %q quit %v", started, quit)
	}
	if b, _ := os.ReadFile(want); string(b) != string(exe) {
		t.Fatalf("%q", b)
	}
	if managed.Verify(want) != nil {
		t.Fatal("no .sha256 next to the version")
	}
}

func TestSafeLinks(t *testing.T) {
	var opened []string
	a := &App{Server: "https://crm.example.ru", Repo: "o/r", P: Platform{OpenURL: func(u string) error { opened = append(opened, u); return nil }}}
	for _, p := range []string{"/team/1", "https://evil.example/x", "//evil.example", "javascript:alert(1)", "/a\r\nb"} {
		a.open(p)
	}
	want := []string{"https://crm.example.ru/team/1", "https://crm.example.ru/team", "https://crm.example.ru/team", "https://crm.example.ru/team", "https://crm.example.ru/team"}
	if strings.Join(opened, " ") != strings.Join(want, " ") {
		t.Fatalf("%v", opened)
	}
	opened = nil
	a.openRelease("https://evil.example/release")
	a.openRelease("https://github.com/o/r/releases/tag/v1.0.0")
	if opened[0] != "https://github.com/o/r/releases/latest" || opened[1] != "https://github.com/o/r/releases/tag/v1.0.0" {
		t.Fatalf("%v", opened)
	}
}

func TestPlainHTTPOnlyInsideTheNetwork(t *testing.T) {
	for _, ok := range []string{"http://localhost:8780", "http://10.27.0.100:8780", "http://192.168.1.5", "http://127.0.0.1"} {
		if _, err := NormalizeServer(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://crm.example.ru", "http://8.8.8.8"} {
		if _, err := NormalizeServer(bad); err == nil {
			t.Errorf("%s accepted over plain http", bad)
		}
	}
}
