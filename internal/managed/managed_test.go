package managed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Load(dir); ok || err != nil {
		t.Fatal(ok, err)
	}
	write(t, filepath.Join(dir, ConfigName), "\xef\xbb\xbf"+`{"server":"https://crm.example.ru"}`)
	c, ok, err := Load(dir)
	if !ok || err != nil || c.Server != "https://crm.example.ru" || c.Managed() {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	write(t, filepath.Join(dir, ConfigName), "{")
	if _, ok, err := Load(dir); ok || err == nil {
		t.Fatal("broken file", ok, err)
	}
}

// The file the app writes holds examples in comments and still reads back; a // inside a value stays.
func TestConfigWithComments(t *testing.T) {
	dir := t.TempDir()
	want := Config{Server: "https://crm.example.ru", UpdateSource: `\\server\apps\AshCRM`}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(dir)
	if !ok || err != nil || got != want || !got.Managed() {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ConfigName))
	for _, part := range []string{"// server", "// update_source", "// disabled", GitHubSource} {
		if !strings.Contains(string(b), part) {
			t.Errorf("no %q", part)
		}
	}
}

func TestUpdateSource(t *testing.T) {
	dir := t.TempDir()
	for source, managedCopy := range map[string]bool{
		"": false, GitHubSource: false, GitHubSource + "/": false, GitHubSource + ".git": false,
		`C:\\ProgramData\\AshCRM`: true,
	} {
		write(t, filepath.Join(dir, ConfigName), `{"update_source":"`+source+`"}`)
		c, ok, err := Load(dir)
		if !ok || err != nil || c.Managed() != managedCopy {
			t.Errorf("%q: %+v %v", source, c, err)
		}
	}
	for _, stranger := range []string{"https://evil.example.ru/app", "https://github.com/someone/else", "http://10.0.0.5/app"} {
		write(t, filepath.Join(dir, ConfigName), `{"update_source":"`+stranger+`"}`)
		if _, ok, err := Load(dir); ok || err == nil {
			t.Errorf("%q accepted", stranger)
		}
	}
}

func TestReadSum(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadSum(dir); err == nil {
		t.Fatal("no file")
	}
	write(t, filepath.Join(dir, ExeName+".sha256"), "\n")
	if _, err := ReadSum(dir); err == nil {
		t.Fatal("empty file")
	}
	sum := strings.Repeat("a", 64)
	write(t, filepath.Join(dir, ExeName+".sha256"), sum+"  "+ExeName+"\n")
	if got, err := ReadSum(dir); err != nil || got != sum {
		t.Fatal(got, err)
	}
}

func TestFetchKeepsOnlyTheRightFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	write(t, src, "new")
	if err := Fetch(src, dst, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong sum accepted")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("a wrong file stays")
	}
	sum, _ := Sum(src)
	if err := Fetch(src, dst, sum); err != nil {
		t.Fatal(err)
	}
}

func TestInstall(t *testing.T) {
	src, home := t.TempDir(), filepath.Join(t.TempDir(), "home")
	exe := filepath.Join(src, ExeName)
	write(t, exe, "v1")
	c := Config{Server: "https://crm.example.ru", UpdateSource: src}
	if changed, err := Install(exe, c, home); !changed || err != nil {
		t.Fatal(changed, err)
	}
	sum, _ := Sum(exe)
	if got, err := ReadSum(home); err != nil || got != sum {
		t.Fatal("the sum goes with the file", got, err)
	}
	if got, ok, _ := Load(home); !ok || got != c {
		t.Fatalf("%+v", got)
	}
	if changed, err := Install(exe, c, home); changed || err != nil {
		t.Fatal("nothing new", changed, err)
	}
	write(t, exe, "v2")
	if changed, err := Install(exe, c, home); !changed || err != nil {
		t.Fatal(changed, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ExeName)); string(b) != "v2" {
		t.Fatal(string(b))
	}
	c.Server = "https://other.example.ru"
	if changed, err := Install(exe, c, home); !changed || err != nil {
		t.Fatal("new settings", changed, err)
	}
}
