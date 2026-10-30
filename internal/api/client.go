package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Error is an answer of the API with an error.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("HTTP %d", e.Status)
}

// SignedOut: the session is over (revoked in «Мои устройства», expired, the account blocked) — sign in again.
func (e *Error) SignedOut() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Client calls the CRM.
type Client struct {
	Base      string // https://crm.example.ru
	HTTP      *http.Client
	UserAgent string
}

// New creates a client of a server.
func New(base, userAgent string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 20 * time.Second}, UserAgent: userAgent}
}

func (c *Client) do(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		e := &Error{Status: res.StatusCode}
		var w struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &w) == nil {
			e.Code, e.Message = w.Error.Code, w.Error.Message
		}
		return e
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Token exchanges the code of the browser sign-in and the PKCE verifier for the session of the app.
func (c *Client) Token(ctx context.Context, code, verifier string) (Tokens, error) {
	var t Tokens
	return t, c.do(ctx, "POST", "/auth/desktop/token", "", map[string]string{"code": code, "verifier": verifier}, &t)
}

// Refresh rotates the refresh token (the old one is spent).
func (c *Client) Refresh(ctx context.Context, refresh string) (Tokens, error) {
	var t Tokens
	return t, c.do(ctx, "POST", "/auth/desktop/refresh", "", map[string]string{"refresh_token": refresh}, &t)
}

// Me returns the signed-in employee.
func (c *Client) Me(ctx context.Context, access string) (Me, error) {
	var m Me
	return m, c.do(ctx, "GET", "/me", access, nil, &m)
}

// Logout ends the session of the app in the CRM.
func (c *Client) Logout(ctx context.Context, access string) error {
	return c.do(ctx, "POST", "/auth/logout", access, nil, nil)
}
