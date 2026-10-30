package stream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

type rec struct {
	mu     sync.Mutex
	syncs  []api.Snapshot
	cards  []api.Card
	clears [][]string
	states []string
}

func (r *rec) Sync(s api.Snapshot) { r.mu.Lock(); r.syncs = append(r.syncs, s); r.mu.Unlock() }
func (r *rec) Card(c api.Card)     { r.mu.Lock(); r.cards = append(r.cards, c); r.mu.Unlock() }
func (r *rec) Clear(k []string)    { r.mu.Lock(); r.clears = append(r.clears, k); r.mu.Unlock() }
func (r *rec) State(s string)      { r.mu.Lock(); r.states = append(r.states, s); r.mu.Unlock() }
func (r *rec) count() (int, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.syncs), len(r.cards), len(r.clears)
}

// fakeCRM speaks the protocol of /ws: auth, notify.snapshot, notify.read, ping; then pushes events.
type fakeCRM struct {
	mu        sync.Mutex
	conns     int
	reads     []string
	closeWith websocket.StatusCode
	tokens    []string
}

func (f *fakeCRM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conns++
	n := f.conns
	closeWith := f.closeWith
	f.mu.Unlock()
	ctx := r.Context()
	send := func(v any) { b, _ := json.Marshal(v); _ = c.Write(ctx, websocket.MessageText, b) }
	ok := true
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var in struct {
			ID   string          `json:"id"`
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(raw, &in)
		switch in.Type {
		case "auth":
			var d struct{ Token string }
			_ = json.Unmarshal(in.Data, &d)
			f.mu.Lock()
			f.tokens = append(f.tokens, d.Token)
			f.mu.Unlock()
			if closeWith != 0 {
				_ = c.Close(closeWith, "no")
				return
			}
			send(map[string]any{"re": in.ID, "ok": ok})
			send(map[string]any{"type": "hello", "data": map[string]any{}})
		case "notify.snapshot":
			send(map[string]any{"re": in.ID, "ok": ok, "data": api.Snapshot{Cards: []api.Card{{Key: "chat-1", Count: 2}}, Total: 1}})
			send(map[string]any{"type": "notify.card", "data": api.Card{Key: "chat-2", Title: "Мария", Count: 1}})
			send(map[string]any{"type": "notify.clear", "data": map[string]any{"keys": []string{"chat-1"}}})
			send(map[string]any{"type": "notify.sync", "data": api.Snapshot{Cards: []api.Card{{Key: "chat-2"}}, Total: 1}})
			if n == 1 {
				go func() { time.Sleep(200 * time.Millisecond); _ = c.Close(websocket.StatusGoingAway, "restart") }()
			}
		case "notify.read":
			var d struct{ Key string }
			_ = json.Unmarshal(in.Data, &d)
			f.mu.Lock()
			f.reads = append(f.reads, d.Key)
			f.mu.Unlock()
			send(map[string]any{"re": in.ID, "ok": ok, "data": map[string]any{"ok": true}})
		default:
			send(map[string]any{"re": in.ID, "ok": ok, "data": map[string]any{}})
		}
	}
}

func TestStreamSnapshotEventsReconnect(t *testing.T) {
	crm := &fakeCRM{}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	h := &rec{}
	s := &Stream{URL: WSURL(srv.URL), H: h, Tokens: func(context.Context, bool) (string, error) { return "tok", nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		crm.mu.Lock()
		n := crm.conns
		crm.mu.Unlock()
		syncs, cards, _ := h.count()
		if n >= 2 && syncs >= 4 && cards >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	syncs, cards, clears := h.count()
	if syncs < 4 || cards < 2 || clears < 2 {
		t.Fatalf("after reconnect: syncs %d cards %d clears %d", syncs, cards, clears)
	}
	if h.syncs[0].Cards[0].Key != "chat-1" || h.cards[0].Title != "Мария" || h.clears[0][0] != "chat-1" {
		t.Fatalf("%+v %+v %+v", h.syncs[0], h.cards[0], h.clears[0])
	}
	if err := s.Read(ctx, "chat-2"); err != nil {
		t.Fatal(err)
	}
	crm.mu.Lock()
	reads := crm.reads
	crm.mu.Unlock()
	if len(reads) != 1 || reads[0] != "chat-2" {
		t.Fatalf("reads %v", reads)
	}
	if !strings.Contains(strings.Join(h.states, ","), StateOnline) {
		t.Fatalf("states %v", h.states)
	}
}

// A revoked session (the CRM closes with 4003 and the refresh is refused) ends the stream: sign in again.
func TestStreamStopsWhenSignedOut(t *testing.T) {
	crm := &fakeCRM{closeWith: closeUnauthorized}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	h := &rec{}
	forced := 0
	s := &Stream{URL: WSURL(srv.URL), H: h, Tokens: func(_ context.Context, force bool) (string, error) {
		if force {
			forced++
			return "", &api.Error{Status: 401}
		}
		return "tok", nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("got %v", err)
	}
	if forced != 1 || h.states[len(h.states)-1] != StateSignedOut {
		t.Fatalf("forced %d states %v", forced, h.states)
	}
}

func TestWSURL(t *testing.T) {
	if WSURL("https://crm.ru/") != "wss://crm.ru/ws?v=1" || WSURL("http://localhost:8780") != "ws://localhost:8780/ws?v=1" {
		t.Fatal(WSURL("https://crm.ru/"))
	}
}
