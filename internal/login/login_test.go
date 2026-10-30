package login

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

// The browser part played by the test: the page of the CRM sends the code back to http://127.0.0.1:<port>/callback.
func browser(t *testing.T, code string, keepState bool) func(string) error {
	return func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		if u.Path != "/desktop-connect" || len(q.Get("challenge")) != 43 || q.Get("device") != "PC-1" {
			t.Errorf("connect page: %s", raw)
		}
		state := q.Get("state")
		if !keepState {
			state = "forged"
		}
		go func() {
			res, err := http.Get("http://127.0.0.1:" + q.Get("port") + "/callback?code=" + code + "&state=" + state)
			if err == nil {
				res.Body.Close()
			}
		}()
		return nil
	}
}

func TestLoopbackSignIn(t *testing.T) {
	r, err := Run(context.Background(), "https://crm.example.ru/", "PC-1", browser(t, "the-code", true))
	if err != nil || r.Code != "the-code" || len(r.Verifier) != 43 {
		t.Fatalf("%+v %v", r, err)
	}
	if Challenge(r.Verifier) == r.Verifier || len(Challenge(r.Verifier)) != 43 {
		t.Fatal("challenge")
	}
}

func TestForgedStateIsRefused(t *testing.T) {
	if _, err := Run(context.Background(), "https://crm.example.ru", "PC-1", browser(t, "x", false)); err != ErrCanceled {
		t.Fatalf("got %v", err)
	}
}
