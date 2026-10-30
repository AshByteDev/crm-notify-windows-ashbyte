// Package login signs the app in through the browser (RFC 8252: a loopback redirect + PKCE): the password never
// reaches the app, 2FA and the sign-in itself stay in the CRM.
package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Timeout of waiting for the person in the browser.
const Timeout = 5 * time.Minute

// ErrCanceled: the browser came back without a code (or the wait was stopped).
var ErrCanceled = errors.New("вход отменён")

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is base64url(sha256(verifier)).
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Result of the browser part: the code to exchange with the verifier.
type Result struct {
	Code     string
	Verifier string
}

const donePage = `<!doctype html><html lang="ru"><meta charset="utf-8"><title>AshCRM</title>
<body style="font-family:Segoe UI,sans-serif;display:flex;align-items:center;justify-content:center;height:90vh;color:#222">
<div style="text-align:center"><h2>%s</h2><p>%s</p></div></body></html>`

// Run listens on 127.0.0.1, opens the page of the CRM in the browser (open) and waits for the code.
func Run(ctx context.Context, server, device string, open func(string) error) (Result, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Result{}, err
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	verifier, state := random(32), random(16)
	got := make(chan Result, 1)
	fail := make(chan error, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || q.Get("code") == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, donePage, "Не получилось", "Нажмите «Войти» в приложении ещё раз.")
			select {
			case fail <- ErrCanceled:
			default:
			}
			return
		}
		fmt.Fprintf(w, donePage, "Готово", "Приложение AshCRM подключено — эту вкладку можно закрыть.")
		select {
		case got <- Result{Code: q.Get("code"), Verifier: verifier}:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = srv.Shutdown(sctx)
		cancel()
	}()
	q := url.Values{"challenge": {Challenge(verifier)}, "state": {state}, "port": {fmt.Sprint(port)}, "device": {device}}
	if err := open(strings.TrimRight(server, "/") + "/desktop-connect?" + q.Encode()); err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	select {
	case r := <-got:
		return r, nil
	case err := <-fail:
		return Result{}, err
	case <-ctx.Done():
		return Result{}, ErrCanceled
	}
}
