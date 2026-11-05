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

// version puts a correct file of a version into a folder.
func version(t *testing.T, dir, v string) string {
	t.Helper()
	body := "exe " + v
	path := filepath.Join(dir, FileName(v))
	write(t, path, body)
	sum, _ := Sum(path)
	write(t, path+".sha256", sum+"  "+FileName(v)+"\n")
	return path
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := Load(dir); ok || err != nil {
		t.Fatal(ok, err)
	}
	write(t, filepath.Join(dir, ConfigName), bom+`{"server":"https://crm.example.ru"}`)
	c, ok, err := Load(dir)
	if !ok || err != nil || c.Server != "https://crm.example.ru" || c.UpdateCheckMinutes != DefaultMinutes {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	write(t, filepath.Join(dir, ConfigName), `{"update_check_minutes":0,"version":"v0.0.10"}`)
	if c, _, _ := Load(dir); c.UpdateCheckMinutes != 0 || c.Version != "0.0.10" {
		t.Fatalf("%+v", c)
	}
	write(t, filepath.Join(dir, ConfigName), "{")
	if _, ok, err := Load(dir); ok || err == nil {
		t.Fatal("broken file", ok, err)
	}
}

// The file the app writes holds examples in comments and still reads back; a // inside a value stays.
func TestConfigWithComments(t *testing.T) {
	dir := t.TempDir()
	want := Config{Server: "https://crm.example.ru", UpdateCheckMinutes: 15, Version: "0.0.11"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := Load(dir); !ok || err != nil || got != want {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ConfigName))
	for _, part := range []string{"// server", "// update_check_minutes", "// version", "// disabled"} {
		if !strings.Contains(string(b), part) {
			t.Errorf("no %q", part)
		}
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	version(t, dir, "0.0.9")
	version(t, dir, "0.0.10")
	version(t, dir, "0.1.0")
	write(t, filepath.Join(dir, ExeName), "launcher")
	write(t, filepath.Join(dir, "AshCRM-Notify-x.exe"), "other")
	got := ""
	for _, f := range List(dir) {
		got += f.Version + " "
	}
	if got != "0.1.0 0.0.10 0.0.9 " {
		t.Fatal(got)
	}
}

func TestIsVersion(t *testing.T) {
	if !IsVersion(`/x/`+FileName("0.0.10")) || IsVersion(`/x/`+ExeName) || IsVersion("/x/AshCRM-Notify (1).exe") {
		t.Fatal("names")
	}
}

func TestTarget(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Target(dir, "", "0.0.5"); ok {
		t.Fatal("empty folder")
	}
	version(t, dir, "0.0.9")
	bad := version(t, dir, "0.0.11")
	write(t, bad, "changed")
	if f, ok := Target(dir, "", "0.0.5"); !ok || f.Version != "0.0.9" {
		t.Fatalf("a wrong file is passed over: %+v", f)
	}
	if _, ok := Target(dir, "", "0.0.10"); ok {
		t.Fatal("older than the running one")
	}
	if f, ok := Target(dir, "", "0.0.9"); !ok || f.Version != "0.0.9" {
		t.Fatalf("the same version: %+v", f)
	}
	if f, ok := Target(dir, "0.0.9", "0.0.12"); !ok || f.Version != "0.0.9" {
		t.Fatalf("a pin goes back: %+v", f)
	}
	if _, ok := Target(dir, "0.0.3", "0.0.12"); ok {
		t.Fatal("no such version")
	}
}

func TestVerifyEmptySum(t *testing.T) {
	path := version(t, t.TempDir(), "0.0.9")
	write(t, path+".sha256", "\n")
	if Verify(path) == nil {
		t.Fatal("an empty sum")
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(t.TempDir(), "x")
	write(t, src, "new")
	sum, _ := Sum(src)
	f, _ := os.Open(src)
	defer f.Close()
	if _, err := Store(dir, "0.0.9", strings.Repeat("0", 64), f); err == nil {
		t.Fatal("a wrong sum accepted")
	}
	if len(List(dir)) != 0 || exists(filepath.Join(dir, FileName("0.0.9"))+".sha256") {
		t.Fatal("a wrong file stays")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.tmp")); len(left) != 0 {
		t.Fatal(left)
	}
	_, _ = f.Seek(0, 0)
	path, err := Store(dir, "0.0.9", sum, f)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != FileName("0.0.9") || Verify(path) != nil {
		t.Fatal(path, Verify(path))
	}
}

func TestCleanup(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"0.0.5", "0.0.6", "0.0.7", "0.0.8", "0.0.9"} {
		version(t, dir, v)
	}
	write(t, filepath.Join(dir, ExeName), "launcher")
	write(t, filepath.Join(dir, "AshCRM-Notify-123.tmp"), "half")
	Cleanup(dir, "0.0.5", "0.0.6")
	for v, want := range map[string]bool{"0.0.5": true, "0.0.6": true, "0.0.7": false, "0.0.8": true, "0.0.9": true} {
		path := filepath.Join(dir, FileName(v))
		if exists(path) != want || exists(path+".sha256") != want {
			t.Errorf("%s: %v", v, exists(path))
		}
	}
	if !exists(filepath.Join(dir, ExeName)) {
		t.Fatal("the constant file is never removed")
	}
	if exists(filepath.Join(dir, "AshCRM-Notify-123.tmp")) {
		t.Fatal("a half-written file stays")
	}
}

func TestWritable(t *testing.T) {
	dir := t.TempDir()
	if !Writable(dir) {
		t.Fatal("a temp folder")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatal(left)
	}
	if Writable(filepath.Join(dir, "none")) {
		t.Fatal("no folder")
	}
}
