package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
)

// NormalizeServer turns what the person typed into the address of the portal: «crm.example.ru», «https://crm.example.ru/team»
// → «https://crm.example.ru» (https when no scheme is given; a page of the CRM is cut to its portal).
func NormalizeServer(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("введите адрес портала")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(u.Host, " \\") {
		return "", errors.New("неверный адрес портала")
	}
	return u.Scheme + "://" + u.Host, nil
}

// probe asks the portal the way the app will: an empty code exchange must be refused by AshCRM with its error.
func probe(ctx context.Context, server string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server+"/api/v1/auth/desktop/token", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("портал не отвечает: %v", err)
	}
	defer res.Body.Close()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	isCRM := json.NewDecoder(res.Body).Decode(&body) == nil && body.Error.Code != ""
	switch {
	case isCRM && (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusUnprocessableEntity ||
		res.StatusCode == http.StatusBadRequest):
		return nil
	case isCRM && res.StatusCode == http.StatusNotFound:
		return errors.New("на портале старая версия AshCRM — приложение работает с 0.0.40 и новее")
	}
	return errors.New("по этому адресу не найден портал AshCRM")
}

// SetServer checks and keeps the portal, then starts the sign-in in the browser. Blocks while checking (call it in
// the background).
func (a *App) SetServer(ctx context.Context, raw string) error {
	server, err := NormalizeServer(raw)
	if err != nil {
		return err
	}
	if err := probe(ctx, server); err != nil {
		return err
	}
	a.mu.Lock()
	a.Server, a.cfg.Server = server, server
	a.api = api.New(server, a.userAgent())
	a.state, a.lastError = SignedOut, ""
	cfg := a.cfg
	a.mu.Unlock()
	if err := platform.SaveConfig(a.Dir, cfg); err != nil {
		return err
	}
	a.changed()
	a.Login(ctx)
	return nil
}

// ChangeServer signs out and asks the portal again.
func (a *App) ChangeServer(ctx context.Context) {
	a.Logout(ctx)
	a.mu.Lock()
	a.state = NeedServer
	a.mu.Unlock()
	a.changed()
}
