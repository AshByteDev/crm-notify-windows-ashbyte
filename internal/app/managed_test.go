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
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/queue"
)

// copyIn is a running copy of version 0.0.5 in a folder; started collects what it starts.
func copyIn(t *testing.T) (a *App, dir string, started *[]string, quit *bool) {
	t.Helper()
	dir = t.TempDir()
	started, quit = &[]string{}, new(bool)
	exe := filepath.Join(dir, managed.ExeName)
	if err := os.WriteFile(exe, []byte("launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	a = &App{Version: "0.0.5", Dir: t.TempDir(), Exe: exe, Launcher: exe, Q: queue.New(), Quit: func() { *quit = true },
		P: Platform{Start: func(path string, args ...string) error {
			*started = append(*started, path+" "+strings.Join(args, " "))
			return nil
		}}}
	return a, dir, started, quit
}

// place is what the scheduled task leaves: a file of a version with its .sha256.
func place(t *testing.T, dir, version string) string {
	t.Helper()
	body := "exe " + version
	sum := sha256.Sum256([]byte(body))
	path, err := managed.Store(dir, version, hex.EncodeToString(sum[:]), strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMovesToNewVersionBeside(t *testing.T) {
	a, dir, started, quit := copyIn(t)
	a.Shared = true
	path := place(t, dir, "0.0.6")
	a.tick(context.Background())
	if len(*started) != 1 || !*quit {
		t.Fatal(*started, *quit)
	}
	if want := path + " --updated --from " + a.Exe; (*started)[0] != want {
		t.Fatal((*started)[0])
	}
}

func TestStaysOnTheSameVersion(t *testing.T) {
	a, dir, started, quit := copyIn(t)
	a.Shared = true
	place(t, dir, "0.0.5")
	place(t, dir, "0.0.4")
	a.tick(context.Background())
	if len(*started) != 0 || *quit {
		t.Fatal(*started, *quit)
	}
}

func TestPinGoesBack(t *testing.T) {
	a, dir, started, _ := copyIn(t)
	a.Shared = true
	place(t, dir, "0.0.6")
	old := place(t, dir, "0.0.4")
	if err := managed.Save(dir, managed.Config{Version: "0.0.4", UpdateCheckMinutes: 60}); err != nil {
		t.Fatal(err)
	}
	a.tick(context.Background())
	if len(*started) != 1 || !strings.HasPrefix((*started)[0], old) {
		t.Fatal(*started)
	}
}

// A half-copied or changed file is never started.
func TestWrongFileIsNotStarted(t *testing.T) {
	a, dir, started, _ := copyIn(t)
	a.Shared = true
	path := place(t, dir, "0.0.6")
	if err := os.WriteFile(path, []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.tick(context.Background())
	if len(*started) != 0 {
		t.Fatal(*started)
	}
}

// release is a fake GitHub with one version.
func release(t *testing.T, version string, asked *[]string) {
	t.Helper()
	body := "exe " + version
	sum := sha256.Sum256([]byte(body))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*asked = append(*asked, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			fmt.Fprintf(w, `{"tag_name":"v%s","assets":[{"name":"AshCRM-Notify.exe","browser_download_url":"%s/exe","digest":"sha256:%s"}]}`,
				version, srv.URL, hex.EncodeToString(sum[:]))
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	GitHubAPI, downloadPrefix = srv.URL, srv.URL+"/"
}

func TestFetchStoresAndCleans(t *testing.T) {
	a, dir, _, quit := copyIn(t)
	a.Repo = "o/r"
	var asked []string
	release(t, "0.0.9", &asked)
	for _, v := range []string{"0.0.2", "0.0.3", "0.0.4"} {
		place(t, dir, v)
	}
	if err := a.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, f := range managed.List(dir) {
		got += f.Version + " "
	}
	if got != "0.0.9 0.0.4 " {
		t.Fatal(got)
	}
	if *quit {
		t.Fatal("the task only fetches")
	}
	asked = nil
	if err := a.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 {
		t.Fatalf("a file already there is not fetched: %v", asked)
	}
}

func TestPinFetchesThatVersion(t *testing.T) {
	a, dir, _, _ := copyIn(t)
	a.Repo = "o/r"
	var asked []string
	release(t, "0.0.2", &asked)
	a.Settings = &managed.Config{Version: "0.0.2"}
	if err := a.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if asked[0] != "/repos/o/r/releases/tags/v0.0.2" {
		t.Fatal(asked)
	}
	if managed.Verify(filepath.Join(dir, managed.FileName("0.0.2"))) != nil {
		t.Fatal("no file")
	}
}

// Without the right to write the app does not even ask: the task does that.
func TestSharedCopyAsksNoGitHub(t *testing.T) {
	a, _, _, _ := copyIn(t)
	a.Repo, a.Shared = "o/r", true
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("GitHub asked") }))
	defer gh.Close()
	GitHubAPI = gh.URL
	a.tick(context.Background())
	if r, err := a.CheckUpdate(context.Background()); r != nil || err != nil {
		t.Fatal(r, err)
	}
}

func TestOrdinaryCopyGetsCardAndCleans(t *testing.T) {
	a, dir, started, _ := copyIn(t)
	a.Repo = "o/r"
	a.cfg.CheckUpdates = true
	var asked []string
	release(t, "0.0.9", &asked)
	for _, v := range []string{"0.0.1", "0.0.2", "0.0.3"} {
		place(t, dir, v)
	}
	a.tick(context.Background())
	if _, ok := a.Q.Get(UpdateKey); !ok {
		t.Fatal("no card")
	}
	if len(*started) != 0 {
		t.Fatal("the person decides", *started)
	}
	if n := len(managed.List(dir)); n != 2 {
		t.Fatal(n)
	}
}

func TestZeroMinutesChecksOnce(t *testing.T) {
	a, dir, started, _ := copyIn(t)
	a.Shared = true
	a.Settings = &managed.Config{UpdateCheckMinutes: 0}
	if err := managed.Save(dir, *a.Settings); err != nil {
		t.Fatal(err)
	}
	place(t, dir, "0.0.6")
	done := make(chan struct{})
	go func() { a.follow(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop goes on")
	}
	if len(*started) != 1 {
		t.Fatal(*started)
	}
}

func TestDisabledRemovesTheApp(t *testing.T) {
	a, dir, _, quit := copyIn(t)
	removed := false
	a.P.Uninstall = func() error { removed = true; return nil }
	if err := managed.Save(dir, managed.Config{Disabled: true}); err != nil {
		t.Fatal(err)
	}
	a.tick(context.Background())
	if !removed || !*quit {
		t.Fatal(removed, *quit)
	}
}

func TestNewPortalInConfig(t *testing.T) {
	a, dir, _, _ := copyIn(t)
	a.Shared = true
	a.Server = "https://old.example.ru"
	if err := managed.Save(dir, managed.Config{Server: "https://new.example.ru"}); err != nil {
		t.Fatal(err)
	}
	a.tick(context.Background())
	v := a.View()
	if v.Server != "https://new.example.ru" || v.State != SignedOut || !v.FixedServer || !v.Shared {
		t.Fatalf("%+v", v)
	}
	if platform.LoadConfig(a.Dir).Server != v.Server {
		t.Fatal("the portal is not saved")
	}
}

// The file is written after a portal is checked and keeps the other settings.
func TestConfigAppearsAfterPortal(t *testing.T) {
	crm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized"}}`))
	}))
	defer crm.Close()
	home := t.TempDir()
	a := &App{Version: "1", Dir: t.TempDir(), Exe: filepath.Join(home, managed.ExeName),
		P: Platform{OpenURL: func(string) error { return nil }}}
	a.Load()
	if _, ok, _ := managed.Load(home); ok {
		t.Fatal("no portal yet")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.SetServer(ctx, "http://127.0.0.1:1"); err == nil {
		t.Fatal("an unreachable portal is accepted")
	}
	if _, ok, _ := managed.Load(home); ok {
		t.Fatal("a failed check writes nothing")
	}
	if err := a.SetServer(ctx, crm.URL); err != nil {
		t.Fatal(err)
	}
	a.CancelLogin()
	c, ok, err := managed.Load(home)
	if !ok || err != nil || c.Server != crm.URL || c.UpdateCheckMinutes != managed.DefaultMinutes {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	if v := a.View(); v.Shared || v.FixedServer {
		t.Fatalf("%+v", v)
	}
}

// A saved portal from an earlier version gets its file on the next start.
func TestConfigAppearsForSavedPortal(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	if err := platform.SaveConfig(dir, platform.Config{Server: "https://crm.example.ru"}); err != nil {
		t.Fatal(err)
	}
	a := &App{Version: "1", Dir: dir, Exe: filepath.Join(home, managed.ExeName)}
	a.Load()
	a.keepBeside()
	if c, ok, _ := managed.Load(home); !ok || c.Server != "https://crm.example.ru" {
		t.Fatalf("%+v", c)
	}
}

func TestSettingsPortalReplacesSaved(t *testing.T) {
	dir := t.TempDir()
	if err := platform.SaveConfig(dir, platform.Config{Server: "https://old.example.ru"}); err != nil {
		t.Fatal(err)
	}
	if err := platform.SaveSecret(dir, platform.Secret{RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	a := &App{Dir: dir, Settings: &managed.Config{Server: "crm.example.ru"}}
	a.Load()
	if a.Server != "https://crm.example.ru" || a.state != SignedOut || a.secret.RefreshToken != "" {
		t.Fatalf("%q %q %q", a.Server, a.state, a.secret.RefreshToken)
	}
}

func TestPauseClosesTheSocket(t *testing.T) {
	crm := &readCRM{at: []time.Time{time.Now()}}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := online(t, ctx, srv.URL)
	a.secret.RefreshToken = "r"
	a.Pause()
	if v := a.View(); v.State != Offline {
		t.Fatal(v.State)
	}
	a.mu.Lock()
	s := a.stream
	a.mu.Unlock()
	if s != nil {
		t.Fatal("the stream stays")
	}
	a.Resume(ctx)
	eventually(t, "connected again", func() bool { c, _ := crm.counts(); return c == 2 })
	a.Resume(ctx)
	time.Sleep(100 * time.Millisecond)
	if c, _ := crm.counts(); c != 2 {
		t.Fatalf("a second resume connects: %d", c)
	}
}

func TestDeviceNameHasUser(t *testing.T) {
	t.Setenv("USERNAME", "ivanov")
	if got := DeviceName(); !strings.HasSuffix(got, " / ivanov") {
		t.Fatal(got)
	}
	t.Setenv("USERNAME", "")
	if got := DeviceName(); strings.Contains(got, "/") {
		t.Fatal(got)
	}
}
