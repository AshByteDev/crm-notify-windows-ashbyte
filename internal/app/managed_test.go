package app

import (
	"context"
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

// managedCopy is a copy in the profile and the folder of the administrator with a new version.
func managedCopy(t *testing.T) (a *App, folder string, replaced *[2]string, quit *bool) {
	t.Helper()
	home, dir := t.TempDir(), t.TempDir()
	folder = t.TempDir()
	exe := filepath.Join(home, managed.ExeName)
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	replaced, quit = &[2]string{}, new(bool)
	a = &App{Version: "1", Dir: dir, Exe: exe, Q: queue.New(), Managed: &managed.Config{UpdateFolder: folder},
		P: Platform{Replace: func(r, d string) error { replaced[0], replaced[1] = r, d; return nil }}, Quit: func() { *quit = true }}
	return a, folder, replaced, quit
}

func publish(t *testing.T, folder, body string) {
	t.Helper()
	exe := filepath.Join(folder, managed.ExeName)
	if err := os.WriteFile(exe, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, _ := managed.Sum(exe)
	if err := os.WriteFile(exe+".sha256", []byte(sum+"  "+managed.ExeName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFolderUpdate(t *testing.T) {
	a, folder, replaced, quit := managedCopy(t)
	publish(t, folder, "v2")
	a.syncFolder(context.Background())
	if !*quit || replaced[0] != a.Exe {
		t.Fatalf("no update: %v %v", *quit, replaced)
	}
	if b, _ := os.ReadFile(replaced[1]); string(b) != "v2" {
		t.Fatal(string(b))
	}
}

// The same file means nothing to do; a file that is still being copied waits for the next check.
func TestFolderUpdateChecksTheFile(t *testing.T) {
	a, folder, _, quit := managedCopy(t)
	publish(t, folder, "v1")
	a.syncFolder(context.Background())
	if *quit {
		t.Fatal("the same version")
	}
	publish(t, folder, "v2")
	if err := os.WriteFile(filepath.Join(folder, managed.ExeName), []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	a.syncFolder(context.Background())
	if *quit {
		t.Fatal("a wrong file is installed")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(a.Exe), managed.NextName)); err == nil {
		t.Fatal("the wrong file stays")
	}
}

func TestFolderMissingIsQuiet(t *testing.T) {
	a, folder, _, quit := managedCopy(t)
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}
	a.syncFolder(context.Background())
	if *quit {
		t.Fatal(*quit)
	}
}

// A managed copy never looks at GitHub.
func TestManagedCheckGoesToFolder(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("GitHub asked") }))
	defer gh.Close()
	GitHubAPI = gh.URL
	a, folder, _, quit := managedCopy(t)
	a.Repo = "x/y"
	publish(t, folder, "v2")
	if r, err := a.CheckUpdate(context.Background()); r != nil || err != nil {
		t.Fatal(r, err)
	}
	if !*quit {
		t.Fatal("the folder was not checked")
	}
}

func TestDisabledRemovesTheApp(t *testing.T) {
	a, folder, _, quit := managedCopy(t)
	removed := false
	a.P.Uninstall = func() error { removed = true; return nil }
	if err := managed.Save(folder, managed.Config{Disabled: true}); err != nil {
		t.Fatal(err)
	}
	a.syncFolder(context.Background())
	if !removed || !*quit {
		t.Fatal(removed, *quit)
	}
}

func TestNewSettingsAreCopiedAndChangePortal(t *testing.T) {
	a, folder, _, _ := managedCopy(t)
	a.Server = "https://old.example.ru"
	want := managed.Config{Server: "https://new.example.ru", UpdateFolder: folder}
	if err := managed.Save(folder, want); err != nil {
		t.Fatal(err)
	}
	a.syncFolder(context.Background())
	if got, ok, _ := managed.Load(filepath.Dir(a.Exe)); !ok || got != want {
		t.Fatalf("%+v", got)
	}
	v := a.View()
	if v.Server != want.Server || v.State != SignedOut || !v.FixedServer || !v.Managed {
		t.Fatalf("%+v", v)
	}
	if platform.LoadConfig(a.Dir).Server != want.Server {
		t.Fatal("the portal is not saved")
	}
}

func TestManagedPortalReplacesSaved(t *testing.T) {
	dir := t.TempDir()
	if err := platform.SaveConfig(dir, platform.Config{Server: "https://old.example.ru"}); err != nil {
		t.Fatal(err)
	}
	if err := platform.SaveSecret(dir, platform.Secret{RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	a := &App{Dir: dir, Managed: &managed.Config{Server: "crm.example.ru"}}
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
