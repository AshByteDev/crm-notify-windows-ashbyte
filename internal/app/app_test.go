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
	crm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/desktop/token" {
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
	if err := probe(ctx, crm.URL); err != nil {
		t.Fatal(err)
	}
	if err := probe(ctx, site.URL); err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("a site: %v", err)
	}
	if err := probe(ctx, old.URL); err == nil || !strings.Contains(err.Error(), "старая версия") {
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
	var replaced string
	quit := false
	a := &App{Version: "0.0.3", Repo: "o/r", Dir: t.TempDir(), Quit: func() { quit = true },
		P: Platform{Replace: func(running, downloaded string) error {
			replaced = downloaded
			b, _ := os.ReadFile(downloaded)
			if string(b) != string(exe) {
				t.Errorf("downloaded %q", b)
			}
			return os.Remove(downloaded)
		}}}
	a.Start(context.Background())
	r, err := a.CheckUpdate(context.Background())
	if err != nil || r == nil || r.Version != "9.9.9" {
		t.Fatalf("%+v %v", r, err)
	}
	if c, ok := a.Q.Get(UpdateKey); !ok || c.Title != "Доступна версия 9.9.9" || c.Body != "Что нового" {
		t.Fatalf("card %+v", c)
	}
	if err := a.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(replaced) != "AshCRM-Notify.new.exe" || !quit {
		t.Fatalf("replaced %q quit %v", replaced, quit)
	}

	// a file that does not match its checksum is not installed
	a.mu.Lock()
	a.update.SHA256 = strings.Repeat("0", 64)
	a.mu.Unlock()
	replaced, quit = "", false
	if err := a.Update(context.Background()); err == nil || replaced != "" || quit {
		t.Fatalf("bad checksum: %v %q %v", err, replaced, quit)
	}
}
