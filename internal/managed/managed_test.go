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
	if !ok || err != nil || c.Server != "https://crm.example.ru" || c.UpdateFolder != dir {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	write(t, filepath.Join(dir, ConfigName), "{")
	if _, ok, err := Load(dir); ok || err == nil {
		t.Fatal("broken file", ok, err)
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
	c := Config{Server: "https://crm.example.ru", UpdateFolder: src}
	if changed, err := Install(exe, c, home); !changed || err != nil {
		t.Fatal(changed, err)
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
