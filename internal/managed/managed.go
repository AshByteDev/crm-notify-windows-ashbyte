// Package managed is config.json next to the .exe and the part of the app an administrator controls. The file names
// the portal and where the updates come from: GitHub by default, or a folder. With a folder the copy is managed:
// started from there, the app copies itself into the profile of the user and later follows it (docs/APP_SPEC.md §7).
// It knows nothing of windows and is tested on any system.
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
	"strings"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
)

const (
	// ExeName is the file of the app, in a release and in the folder of the administrator.
	ExeName = "AshCRM-Notify.exe"
	// ConfigName is the file of settings next to the .exe.
	ConfigName = "config.json"
	// Repo is the repository of the releases.
	Repo = "AshByteDev/crm-notify-windows-ashbyte"
	// GitHubSource is the default source of the updates.
	GitHubSource = "https://github.com/" + Repo
	// NextName is the new version while it is being copied.
	NextName = "AshCRM-Notify.new.exe"

	maxExe = 100 << 20 // bigger is not the app
)

// Config is config.json. The app writes one with the portal by itself, so the file alone means nothing more.
type Config struct {
	Server string `json:"server,omitempty"`
	// UpdateSource is GitHubSource or a folder of the administrator (empty — GitHub). Only a folder makes the copy
	// managed; no other address is accepted: an edited file must not bring updates from a stranger.
	UpdateSource string `json:"update_source,omitempty"`
	// Disabled removes the app from every profile.
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
	b = []byte(strings.TrimPrefix(string(b), "\xef\xbb\xbf")) // Notepad may add the mark
	if err := json.Unmarshal(stripComments(b), &c); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", ConfigName, err)
	}
	if strings.Contains(c.UpdateSource, "://") && !isGitHub(c.UpdateSource) {
		return Config{}, false, fmt.Errorf("%s: update_source — только %s или папка", ConfigName, GitHubSource)
	}
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

func isGitHub(s string) bool {
	return strings.EqualFold(strings.TrimSuffix(strings.TrimRight(s, "/"), ".git"), GitHubSource)
}

// help opens every config.json the app writes.
const help = `// Настройки AshCRM Notify. Строки с // — комментарии, приложение их пропускает.
//
// server — адрес портала AshCRM:
//   "server": "https://crm.company.ru"
//   "server": "http://10.0.0.5:8780"        http только для локальной сети
//
// update_source — откуда брать обновления:
//   "update_source": "` + GitHubSource + `"
//       релизы приложения на GitHub, новая версия предлагается карточкой
//   "update_source": "\\\\server\\apps\\AshCRM"
//       общая папка администратора
//   "update_source": "C:\\ProgramData\\AshCRM"
//       локальная папка администратора
//   Другие адреса не принимаются. Обратные косые черты в пути удваиваются.
//   Из папки приложение обновляется само, без вопросов; в ней лежат AshCRM-Notify.exe,
//   AshCRM-Notify.exe.sha256 и config.json. Писать в папку может только администратор.
//
// disabled — удалить приложение у всех, кто обновляется из этой папки:
//   "disabled": true
`

// Save writes the file into a folder, with the examples.
func Save(dir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	b = append([]byte(help), append(b, '\n')...)
	tmp := filepath.Join(dir, ConfigName+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, ConfigName))
}

// Managed: the updates come from a folder of the administrator.
func (c Config) Managed() bool { return c.UpdateSource != "" && !isGitHub(c.UpdateSource) }

// SaveSum writes the .sha256 next to the file. It is for the people: the app never trusts it, it counts its own sum.
func SaveSum(dir, sum string) error {
	return os.WriteFile(filepath.Join(dir, ExeName+".sha256"), []byte(sum+"  "+ExeName+"\n"), 0o644)
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

// ReadSum reads the .sha256 of a folder.
func ReadSum(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, ExeName+".sha256"))
	if err != nil {
		return "", err
	}
	if f := strings.Fields(strings.TrimPrefix(string(b), "\xef\xbb\xbf")); len(f) > 0 && len(f[0]) == 64 {
		return f[0], nil
	}
	return "", errors.New("в файле .sha256 нет контрольной суммы")
}

// Fetch copies a file and keeps it only when its SHA-256 is the expected one.
func Fetch(src, dst, sum string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, maxExe+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxExe {
		return errors.New("файл слишком большой")
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sum) {
		return errors.New("контрольная сумма не совпала")
	}
	return nil
}

// SameDir compares folders the way Windows does.
func SameDir(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// Install puts the running file and the settings into the folder of the user; changed=false — they are already there.
// A running copy may hold the old file: it is renamed, not overwritten, and the caller closes that copy.
func Install(exe string, c Config, home string) (changed bool, err error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return false, err
	}
	target := filepath.Join(home, ExeName)
	sum, err := Sum(exe)
	if err != nil {
		return false, err
	}
	if have, err := Sum(target); err != nil || !strings.EqualFold(have, sum) {
		next := filepath.Join(home, NextName)
		if err := Fetch(exe, next, sum); err != nil {
			return false, err
		}
		if err := platform.Swap(target, next); err != nil {
			_ = os.Remove(next)
			return false, err
		}
		changed = true
	}
	if err := SaveSum(home, sum); err != nil {
		return false, err
	}
	if have, ok, err := Load(home); err != nil || !ok || have != c {
		if err := Save(home, c); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}
