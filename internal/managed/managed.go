// Package managed is what lies next to the .exe: config.json and the files of the versions. AshCRM-Notify.exe keeps its
// name (the shortcut and autostart point at it) and starts the newest AshCRM-Notify-X.Y.Z.exe next to it. A new version
// is never written over a running file: it arrives as a new file (docs/APP_SPEC.md §7). Since RunSince the version runs
// from a copy with a constant name too, AshCRM-Notify-app.exe: Windows keeps «always show the icon» by the path of the
// file. The package knows nothing of windows and is tested on any system.
package managed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// ExeName is the file of a release and the constant name of the app.
	ExeName = "AshCRM-Notify.exe"
	// RunName is the copy of the version that runs: the same path for every version, so Windows keeps the settings of
	// the icon. The running file is never overwritten: it is renamed aside (Windows allows that) and the new copy takes
	// its name.
	RunName = "AshCRM-Notify-app.exe"
	// RunSince is the first version that runs from RunName; an older one (a pin back) runs from its own file, as before.
	RunSince = "0.0.16"
	// ConfigName is the file of settings next to the .exe.
	ConfigName = "config.json"
	// Repo is the repository of the releases.
	Repo = "AshByteDev/crm-notify-windows-ashbyte"
	// DefaultMinutes is how often the app looks for a new version.
	DefaultMinutes = 60

	keepVersions = 2 // the newest and the one before it
	maxExe       = 100 << 20
	bom          = "\xef\xbb\xbf" // Notepad may add the mark
)

var versioned = regexp.MustCompile(`^AshCRM-Notify-(\d+\.\d+\.\d+)\.exe$`)

// Config is config.json.
type Config struct {
	Server string `json:"server,omitempty"`
	// UpdateCheckMinutes: how often to look for a new version; 0 — only when the app starts.
	UpdateCheckMinutes int `json:"update_check_minutes"`
	// Version holds every copy on one version, older or newer than the latest.
	Version string `json:"version,omitempty"`
	// Disabled removes the app from the profile of every user of this folder.
	Disabled bool `json:"disabled,omitempty"`
}

// Load reads the file of a folder; ok=false — there is none.
func Load(dir string) (c Config, ok bool, err error) {
	b, err := os.ReadFile(filepath.Join(dir, ConfigName))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	c.UpdateCheckMinutes = DefaultMinutes
	if err := json.Unmarshal(stripComments([]byte(strings.TrimPrefix(string(b), bom))), &c); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", ConfigName, err)
	}
	c.Version = strings.TrimPrefix(c.Version, "v")
	return c, true, nil
}

// stripComments drops the lines starting with // (not plain JSON: a person edits this file). A // inside a value stays.
func stripComments(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "//") {
			kept = append(kept, l)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

// help opens every config.json the app writes.
const help = `// Настройки AshCRM Notify. Строки с // — комментарии, приложение их пропускает.
//
// server — адрес портала AshCRM:
//   "server": "https://crm.company.ru"
//   "server": "http://10.0.0.5:8780"        http только для локальной сети
//
// update_check_minutes — как часто искать новую версию:
//   "update_check_minutes": 60              раз в час
//   "update_check_minutes": 0               только при запуске приложения
//
// version — держать всех на одной версии, в том числе вернуться на старую:
//   "version": "0.0.10"
//
// disabled — удалить приложение у всех пользователей этой папки:
//   "disabled": true
`

// Save writes the file into a folder, with the examples.
func Save(dir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	b = append([]byte(help), append(b, '\n')...)
	tmp := filepath.Join(dir, ConfigName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ConfigName))
}

// Newer compares versions "X.Y.Z" (a leading «v» is ignored).
func Newer(v, than string) bool {
	parse := func(s string) [3]int {
		var out [3]int
		for i, p := range strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3) {
			out[i], _ = strconv.Atoi(p)
		}
		return out
	}
	a, b := parse(v), parse(than)
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

// FileName is the file of a version.
func FileName(version string) string { return "AshCRM-Notify-" + version + ".exe" }

// IsVersion: the path is the file of a version, not the constant name.
func IsVersion(path string) bool { return versioned.MatchString(filepath.Base(path)) }

// IsRun: the path is the running copy with the constant name.
func IsRun(path string) bool { return strings.EqualFold(filepath.Base(path), RunName) }

// Found is a file of a version.
type Found struct{ Version, Path string }

// List returns the files of the versions, the newest first.
func List(dir string) []Found {
	entries, _ := os.ReadDir(dir)
	var out []Found
	for _, e := range entries {
		if m := versioned.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() {
			out = append(out, Found{m[1], filepath.Join(dir, e.Name())})
		}
	}
	slices.SortFunc(out, func(a, b Found) int {
		switch {
		case Newer(a.Version, b.Version):
			return -1
		case Newer(b.Version, a.Version):
			return 1
		}
		return 0
	})
	return out
}

// Target is the file to run: the pinned version, or the newest not older than own. A file whose sum does not match is
// passed over.
func Target(dir, pin, own string) (Found, bool) {
	for _, f := range List(dir) {
		if pin != "" && f.Version != pin || pin == "" && Newer(own, f.Version) {
			continue
		}
		if Verify(f.Path) == nil {
			return f, true
		}
	}
	return Found{}, false
}

// Verify checks a file against the .sha256 next to it.
func Verify(path string) error {
	want, err := expected(path)
	if err != nil {
		return err
	}
	have, err := Sum(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(have, want) {
		return errors.New("контрольная сумма не совпала")
	}
	return nil
}

// expected is the sum written in the .sha256 of a file.
func expected(path string) (string, error) {
	b, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return "", err
	}
	f := strings.Fields(strings.TrimPrefix(string(b), bom))
	if len(f) == 0 || len(f[0]) != 64 {
		return "", errors.New("в файле .sha256 нет контрольной суммы")
	}
	return f[0], nil
}

// Prepare is the file to start for a version: RunName when it holds exactly this version (by the sum of its .sha256),
// after putting it there when write allows. Otherwise — an older version, no right to write, a failure — the file of the
// version itself, as before RunSince: the app works, only Windows may forget the icon.
func Prepare(dir string, t Found, write bool) string {
	if Newer(RunSince, t.Version) {
		return t.Path
	}
	want, err := expected(t.Path)
	if err != nil {
		return t.Path
	}
	run := filepath.Join(dir, RunName)
	if have, err := Sum(run); err == nil && strings.EqualFold(have, want) {
		return run
	}
	if !write || install(dir, t.Path, want) != nil {
		return t.Path
	}
	return run
}

// install copies a version into RunName. A copy whose sum differs never takes the name; the running RunName is renamed
// aside first (it cannot be replaced while it runs) and comes back if the new one cannot take its place.
func install(dir, from, want string) (err error) {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(dir, "AshCRM-Notify-app.*.part")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(src, maxExe+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return errors.New("контрольная сумма не совпала")
	}
	if err = os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	run := filepath.Join(dir, RunName)
	aside := ""
	if _, serr := os.Stat(run); serr == nil {
		aside = filepath.Join(dir, fmt.Sprintf("AshCRM-Notify-app.%d.old", time.Now().UnixNano()))
		if err = os.Rename(run, aside); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp.Name(), run); err != nil && aside != "" {
		_ = os.Rename(aside, run)
	}
	return err
}

// Sum is the SHA-256 of a file.
func Sum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Store saves a new version next to the app. It takes its name, the one others look for, only when the sum matches and
// its .sha256 is in place: a half-written file is never started.
func Store(dir, version, sum string, body io.Reader) (path string, err error) {
	path = filepath.Join(dir, FileName(version))
	tmp, err := os.CreateTemp(dir, "AshCRM-Notify-*.tmp")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, maxExe+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if n > maxExe {
		return "", errors.New("файл слишком большой")
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum) {
		return "", errors.New("контрольная сумма не совпала")
	}
	if err = os.WriteFile(path+".sha256", []byte(strings.ToLower(sum)+"  "+FileName(version)+"\n"), 0o644); err != nil {
		return "", err
	}
	if err = os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// Cleanup keeps the two newest versions, the pinned one and the running one, and removes the old copies of RunName. A
// file someone still runs cannot be removed on Windows: it stays until a later pass. Its .sha256 goes only with it.
func Cleanup(dir, pin, own string) {
	for _, pattern := range []string{"AshCRM-Notify-*.tmp", "AshCRM-Notify-app.*.part", "AshCRM-Notify-app.*.old"} {
		if tmp, err := filepath.Glob(filepath.Join(dir, pattern)); err == nil {
			for _, f := range tmp {
				_ = os.Remove(f) // a copy that still runs stays until a later pass
			}
		}
	}
	for i, f := range List(dir) {
		if i < keepVersions || f.Version == pin || f.Version == own {
			continue
		}
		if os.Remove(f.Path) == nil {
			_ = os.Remove(f.Path + ".sha256")
		}
	}
}

// Writable: the app may put files next to itself. On a terminal server, in Program Files, it may not: new versions come
// from the scheduled task and the people only start them.
func Writable(dir string) bool {
	f, err := os.CreateTemp(dir, "AshCRM-Notify-*.tmp")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}
