package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// AssetName is the file of a release (built by .github/workflows/release.yml with its .sha256 next to it).
const AssetName = "AshCRM-Notify.exe"

// GitHubAPI is replaceable in tests.
var GitHubAPI = "https://api.github.com"

// downloadPrefix: a release file is downloaded only from the releases of the repository ("" — github.com/<Repo>).
var downloadPrefix = ""

// Release is a newer version on GitHub.
type Release struct {
	Version string
	Notes   string
	URL     string // the .exe
	SHA256  string // hex
	Page    string // the release page
}

// newer compares versions "X.Y.Z" (a leading «v» is ignored).
func newer(v, than string) bool {
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

func (a *App) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.userAgent())
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("слишком большой ответ")
	}
	return b, nil
}

// latest reads the latest release of the repository; nil — nothing newer.
func (a *App) latest(ctx context.Context) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := a.get(ctx, GitHubAPI+"/repos/"+a.Repo+"/releases/latest", 1<<20)
	if err != nil {
		return nil, err
	}
	var gh struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		Page   string `json:"html_url"`
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"` // "sha256:…"
		} `json:"assets"`
	}
	if err := json.Unmarshal(raw, &gh); err != nil {
		return nil, err
	}
	if !newer(gh.Tag, a.Version) {
		return nil, nil
	}
	r := &Release{Version: strings.TrimPrefix(gh.Tag, "v"), Notes: strings.TrimSpace(gh.Body), Page: gh.Page}
	var sumURL string
	for _, as := range gh.Assets {
		switch as.Name {
		case AssetName:
			r.URL = as.URL
			if d, ok := strings.CutPrefix(as.Digest, "sha256:"); ok {
				r.SHA256 = d
			}
		case AssetName + ".sha256":
			sumURL = as.URL
		}
	}
	if r.URL == "" {
		return nil, errors.New("в релизе нет " + AssetName)
	}
	prefix := downloadPrefix
	if prefix == "" {
		prefix = "https://github.com/" + a.Repo + "/releases/download/"
	}
	if !strings.HasPrefix(r.URL, prefix) {
		return nil, errors.New("файл релиза не из репозитория приложения")
	}
	if r.SHA256 == "" && sumURL != "" {
		if b, err := a.get(ctx, sumURL, 1024); err == nil {
			if f := strings.Fields(string(b)); len(f) > 0 {
				r.SHA256 = f[0]
			}
		}
	}
	if len(r.SHA256) != 64 {
		return nil, errors.New("у релиза нет контрольной суммы")
	}
	return r, nil
}

// CheckUpdate looks for a newer release now (the menu «Проверить обновления») and shows its card.
func (a *App) CheckUpdate(ctx context.Context) (*Release, error) {
	if a.Repo == "" {
		return nil, errors.New("обновления не настроены")
	}
	r, err := a.latest(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.update = r
	a.mu.Unlock()
	if r != nil {
		body := "Нажмите «Обновить» — приложение скачает новую версию и перезапустится."
		if r.Notes != "" {
			body = r.Notes
		}
		a.Q.Local(api.Card{Key: UpdateKey, Kind: "update", Title: "Доступна версия " + r.Version, Body: body, URL: r.Page,
			At: time.Now(), Sound: api.SoundOther})
	}
	a.changed()
	return r, nil
}

// updates checks once a day while «Проверять обновления» is on.
func (a *App) updates(ctx context.Context) {
	if a.Repo == "" {
		return
	}
	for {
		a.mu.Lock()
		on := a.cfg.CheckUpdates
		a.mu.Unlock()
		if on {
			if _, err := a.CheckUpdate(ctx); err != nil {
				a.log().Info("update check", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

// Update downloads the new version, checks its SHA-256, puts it in place of the running file and restarts.
func (a *App) Update(ctx context.Context) error {
	a.mu.Lock()
	r := a.update
	if r == nil || a.updating {
		a.mu.Unlock()
		return nil
	}
	a.updating = true
	a.mu.Unlock()
	a.changed()
	err := a.install(ctx, r)
	a.mu.Lock()
	a.updating = false
	a.mu.Unlock()
	if err != nil {
		a.setState(a.View().State, "обновление не удалось: "+err.Error())
		return err
	}
	if a.Quit != nil {
		a.Quit()
	}
	return nil
}

func (a *App) install(ctx context.Context, r *Release) error {
	if a.P.Replace == nil {
		return errors.New("обновление из приложения есть только в Windows")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", r.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", a.userAgent())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("скачивание: HTTP %d", res.StatusCode)
	}
	// next to the running file: a rename within one folder always works (another disk would not)
	next := filepath.Join(filepath.Dir(exe), "AshCRM-Notify.new.exe")
	f, err := os.OpenFile(next, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("нет прав на папку приложения: %w", err)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), io.LimitReader(res.Body, 100<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(next)
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, r.SHA256) {
		_ = os.Remove(next)
		return errors.New("контрольная сумма не совпала — файл не установлен")
	}
	return a.P.Replace(exe, next)
}
