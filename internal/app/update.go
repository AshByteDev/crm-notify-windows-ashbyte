package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
)

// AssetName is the file of a release (built by .github/workflows/release.yml with its .sha256 next to it). It is saved
// under the name of its version.
const AssetName = managed.ExeName

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

func newer(v, than string) bool { return managed.Newer(v, than) }

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

// latest reads the release to have: the pinned one or the latest; nil — nothing newer than the app.
func (a *App) latest(ctx context.Context, pin string) (*Release, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	page := "/releases/latest"
	if pin != "" {
		page = "/releases/tags/v" + pin
	}
	raw, err := a.get(ctx, GitHubAPI+"/repos/"+a.Repo+page, 1<<20)
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
	if pin == "" && !newer(gh.Tag, a.Version) {
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

// CheckUpdate looks for a newer release now (the menu «Проверить обновления») and shows its card. Without the right to
// write the card would promise what the app cannot do: the scheduled task gets the versions there.
func (a *App) CheckUpdate(ctx context.Context) (*Release, error) {
	if a.Shared {
		return nil, nil
	}
	if a.Repo == "" {
		return nil, errors.New("обновления не настроены")
	}
	r, err := a.latest(ctx, "")
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

// Update downloads the new version next to the app and restarts from it.
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
	path, err := a.store(ctx, r)
	a.mu.Lock()
	a.updating = false
	a.mu.Unlock()
	if err != nil {
		a.setState(a.View().State, "обновление не удалось: "+err.Error())
		return err
	}
	a.restart(path)
	return nil
}

// Fetch brings the pinned or the latest version next to the app and removes the old ones: the work of the scheduled task
// on a terminal server, of the app itself on an ordinary computer with a pinned version.
func (a *App) Fetch(ctx context.Context) error {
	pin := a.pin()
	r, err := a.latest(ctx, pin)
	if err != nil {
		return err
	}
	if r != nil {
		if _, err := a.store(ctx, r); err != nil {
			return err
		}
	}
	managed.Cleanup(a.here(), pin, a.Version)
	return nil
}

// store saves a release under the name of its version; a correct file already there is not fetched again.
func (a *App) store(ctx context.Context, r *Release) (string, error) {
	path := filepath.Join(a.here(), managed.FileName(r.Version))
	if managed.Verify(path) == nil {
		return path, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", r.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", a.userAgent())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("скачивание: HTTP %d", res.StatusCode)
	}
	return managed.Store(a.here(), r.Version, r.SHA256, res.Body)
}
