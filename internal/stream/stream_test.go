package stream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	closeLive websocket.StatusCode // the first socket closes with it after the snapshot
	refuse    bool                 // notify.read is answered with an error
	tokens    []string
	agents    []string
	activity  []string // the data of the notify.activity frames, in order
	refuseAll bool     // notify.activity is answered like by a CRM older than 1.2
}

func (f *fakeCRM) frames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.activity...)
}

func (f *fakeCRM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conns++
	f.agents = append(f.agents, r.Header.Get("User-Agent"))
	n := f.conns
	closeWith, closeLive := f.closeWith, f.closeLive
	f.mu.Unlock()
	if closeLive == 0 {
		closeLive = websocket.StatusGoingAway
	}
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
				go func() { time.Sleep(200 * time.Millisecond); _ = c.Close(closeLive, "restart") }()
			}
		case "notify.read":
			var d struct{ Key string }
			_ = json.Unmarshal(in.Data, &d)
			f.mu.Lock()
			f.reads = append(f.reads, d.Key)
			refuse := f.refuse
			f.mu.Unlock()
			if refuse {
				send(map[string]any{"re": in.ID, "ok": false, "error": map[string]any{"code": "validation", "message": "нет такой карточки"}})
				continue
			}
			send(map[string]any{"re": in.ID, "ok": ok, "data": map[string]any{"ok": true}})
		case "notify.activity":
			f.mu.Lock()
			f.activity = append(f.activity, string(in.Data))
			refuse := f.refuseAll
			f.mu.Unlock()
			if refuse {
				send(map[string]any{"re": in.ID, "ok": false, "error": map[string]any{"code": "forbidden", "message": "нет такого кадра"}})
				continue
			}
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

// A live socket closed with 4001/4003: the session is over only if the refresh says so (contract §4.4).
func TestLiveCloseAsksRefreshFirst(t *testing.T) {
	for _, code := range []websocket.StatusCode{closeSessionRevoked, closeUnauthorized} {
		crm := &fakeCRM{closeLive: code}
		srv := httptest.NewServer(crm)
		h := &rec{}
		s := &Stream{URL: WSURL(srv.URL), H: h, UserAgent: "AshCRM-Notify/1 (Windows)",
			Tokens: func(context.Context, bool) (string, error) { return "tok", nil }}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			crm.mu.Lock()
			n := crm.conns
			crm.mu.Unlock()
			if n >= 2 {
				break
			}
		}
		cancel()
		err := <-done
		srv.Close()
		if errors.Is(err, ErrSignedOut) {
			t.Fatalf("%d with a working refresh signs out", code)
		}
		crm.mu.Lock()
		conns, agent := crm.conns, crm.agents[0]
		crm.mu.Unlock()
		if conns < 2 {
			t.Fatalf("%d: no reconnect", code)
		}
		if agent != "AshCRM-Notify/1 (Windows)" {
			t.Fatalf("User-Agent %q", agent)
		}
	}
}

func TestLiveCloseEndsWhenRefreshRefused(t *testing.T) {
	crm := &fakeCRM{closeLive: closeSessionRevoked}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	s := &Stream{URL: WSURL(srv.URL), H: &rec{}, Tokens: func(_ context.Context, force bool) (string, error) {
		if force {
			return "", &api.Error{Status: 401}
		}
		return "tok", nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Run(ctx); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("got %v", err)
	}
}

// A refused «Прочитано» differs from a lost connection: it is not sent again.
func TestReadRefused(t *testing.T) {
	crm := &fakeCRM{refuse: true}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	h := &rec{}
	s := &Stream{URL: WSURL(srv.URL), H: h, Tokens: func(context.Context, bool) (string, error) { return "tok", nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.Read(ctx, "chat-1"); err == nil || errors.Is(err, ErrRefused) {
		t.Fatalf("no connection yet: %v", err)
	}
	go func() { _ = s.Run(ctx) }()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if n, _, _ := h.count(); n > 0 {
			break
		}
	}
	if err := s.Read(ctx, "chat-1"); !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v", err)
	}
}

func waitFrames(t *testing.T, crm *fakeCRM, want ...string) {
	t.Helper()
	got := crm.frames()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end) && len(got) < len(want); time.Sleep(5 * time.Millisecond) {
		got = crm.frames()
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("frames %v, want %v", got, want)
	}
}

// The state is sent after every snapshot and then only when it changes (contract §5.7).
func TestActivityAfterConnectAndOnChange(t *testing.T) {
	crm := &fakeCRM{closeLive: websocket.StatusGoingAway}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	var active atomic.Bool
	active.Store(true)
	s := &Stream{URL: WSURL(srv.URL), H: &rec{}, every: 10 * time.Millisecond,
		Tokens: func(context.Context, bool) (string, error) { return "tok", nil },
		Active: func() (bool, bool) { return active.Load(), true }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	on, off := `{"active":true}`, `{"active":false}`
	waitFrames(t, crm, on)
	time.Sleep(50 * time.Millisecond)
	if got := crm.frames(); len(got) != 1 {
		t.Fatalf("no change, but %v", got)
	}
	// the first socket closes 200 ms after its snapshot: the new socket is told again, the CRM forgot it with the old one
	waitFrames(t, crm, on, on)
	active.Store(false)
	waitFrames(t, crm, on, on, off)
}

// A CRM older than 1.2 refuses the frame: the connection lives on, the frame is not repeated.
func TestActivityRefused(t *testing.T) {
	crm := &fakeCRM{refuseAll: true, closeLive: websocket.StatusGoingAway}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	h := &rec{}
	s := &Stream{URL: WSURL(srv.URL), H: h, every: 5 * time.Millisecond,
		Tokens: func(context.Context, bool) (string, error) { return "tok", nil },
		Active: func() (bool, bool) { return true, true }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	waitFrames(t, crm, `{"active":true}`)
	time.Sleep(50 * time.Millisecond)
	if got := crm.frames(); len(got) != 1 {
		t.Fatalf("repeated: %v", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.states[len(h.states)-1] != StateOnline {
		t.Fatalf("states %v", h.states)
	}
}

// While the state is unknown nothing is sent: the frame is optional, and a guess would ring the phone or silence it.
func TestActivityUnknownIsNotSent(t *testing.T) {
	crm := &fakeCRM{}
	srv := httptest.NewServer(crm)
	defer srv.Close()
	h := &rec{}
	var known atomic.Bool
	s := &Stream{URL: WSURL(srv.URL), H: h, every: 5 * time.Millisecond,
		Tokens: func(context.Context, bool) (string, error) { return "tok", nil },
		Active: func() (bool, bool) { return false, known.Load() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if n, _, _ := h.count(); n > 0 {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	if got := crm.frames(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
	known.Store(true)
	waitFrames(t, crm, `{"active":false}`)
}

func TestWSURL(t *testing.T) {
	if WSURL("https://crm.ru/") != "wss://crm.ru/ws?v=1" || WSURL("http://localhost:8780") != "ws://localhost:8780/ws?v=1" {
		t.Fatal(WSURL("https://crm.ru/"))
	}
}
