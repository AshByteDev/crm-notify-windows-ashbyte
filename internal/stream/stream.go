// Package stream keeps one WebSocket to the CRM (/ws) and turns its notify.* frames into calls of a Handler: a snapshot
// after every (re)connect, then live cards. No polling: while the socket is alive the server pushes; a broken socket
// is reconnected with a growing pause (crm-ashbyte/docs/WINDOWS_NOTIFICATIONS_CRM_SPEC.md §4).
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// Close codes of the CRM (core/internal/realtime/hub.go).
const (
	closeSessionRevoked = 4001
	closeAuthExpired    = 4002
	closeUnauthorized   = 4003
)

// Connection states for the tray.
const (
	StateConnecting = "connecting"
	StateOnline     = "online"
	StateOffline    = "offline"
	StateSignedOut  = "signed_out"
)

const (
	pingEvery     = 25 * time.Second // the server drops a socket silent for 75 s
	refreshEvery  = 12 * time.Minute // access tokens live 15 minutes
	maxPause      = time.Minute
	activityEvery = 5 * time.Second // how often Active is asked
)

// ErrSignedOut: the session of the app is over — sign in again.
var ErrSignedOut = errors.New("сессия приложения завершена")

// ErrRefused: the CRM answered and refused the request; sending it again changes nothing.
var ErrRefused = errors.New("CRM отклонила запрос")

// Handler receives what the server sends.
type Handler interface {
	Sync(s api.Snapshot) // the snapshot after connecting and notify.sync
	Card(c api.Card)
	Clear(keys []string)
	State(state string)
}

// Tokens gives a valid access token; force — refresh now. ErrSignedOut (or an api.Error that is SignedOut) ends
// the stream.
type Tokens func(ctx context.Context, force bool) (string, error)

// Stream is the connection.
type Stream struct {
	URL       string // wss://crm.example.ru/ws?v=1
	UserAgent string // contract §1
	Tokens    Tokens
	H         Handler
	Log       *slog.Logger
	// Dial is replaceable in tests.
	Dial func(ctx context.Context, url string) (*websocket.Conn, error)
	// Active tells whether the person works at the computer; known=false — it cannot be told now. It is reported to the
	// CRM (contract §5.7); nil — nothing is reported.
	Active func() (active, known bool)
	// every overrides activityEvery in tests.
	every time.Duration

	mu    sync.Mutex
	conn  *websocket.Conn
	seq   atomic.Int64
	waits map[string]chan frame
	kick  chan struct{}

	// events that come before the snapshot is applied wait for it (else an older snapshot would undo them)
	dmu  sync.Mutex
	live bool
	held []frame
}

type frame struct {
	ID    string          `json:"id,omitempty"`
	Type  string          `json:"type,omitempty"`
	Re    string          `json:"re,omitempty"`
	OK    *bool           `json:"ok,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// WSURL turns the address of the CRM into the address of its WebSocket.
func WSURL(server string) string {
	s := strings.TrimRight(server, "/")
	s = strings.Replace(s, "https://", "wss://", 1)
	s = strings.Replace(s, "http://", "ws://", 1)
	return s + "/ws?v=1"
}

func signedOut(err error) bool {
	var ae *api.Error
	return errors.Is(err, ErrSignedOut) || (errors.As(err, &ae) && ae.SignedOut())
}

func (s *Stream) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Reconnect drops the current socket at once (the computer woke up, the network came back). It is called on the UI
// thread: the socket closes in the background, without the closing handshake a dead connection would stall.
func (s *Stream) Reconnect() {
	s.mu.Lock()
	c, k := s.conn, s.kick
	s.mu.Unlock()
	if c != nil {
		go func() { _ = c.CloseNow() }()
	}
	if k != nil {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

// Run connects and reconnects until ctx ends or the session is over (returns ErrSignedOut then).
func (s *Stream) Run(ctx context.Context) error {
	s.mu.Lock()
	s.kick = make(chan struct{}, 1)
	s.mu.Unlock()
	pause := time.Second
	for ctx.Err() == nil {
		s.H.State(StateConnecting)
		started := time.Now()
		err := s.session(ctx)
		if ctx.Err() != nil {
			break
		}
		if signedOut(err) {
			s.H.State(StateSignedOut)
			return ErrSignedOut
		}
		s.H.State(StateOffline)
		s.log().Info("stream: disconnected", "err", err)
		if time.Since(started) > time.Minute {
			pause = time.Second // it worked for a while: start over
		}
		wait := pause + time.Duration(rand.Int64N(int64(pause/2)+1))
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		case <-s.kick:
		}
		pause = min(pause*2, maxPause)
	}
	return ctx.Err()
}

func (s *Stream) dial(ctx context.Context) (*websocket.Conn, error) {
	if s.Dial != nil {
		return s.Dial(ctx, s.URL)
	}
	var opts *websocket.DialOptions
	if s.UserAgent != "" {
		opts = &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": {s.UserAgent}}}
	}
	c, _, err := websocket.Dial(ctx, s.URL, opts)
	return c, err
}

// afterClose follows the close codes of contract §4.4: the session may be alive, only a refused refresh ends it.
func (s *Stream) afterClose(ctx context.Context, err error) error {
	switch websocket.CloseStatus(err) {
	case closeSessionRevoked, closeAuthExpired, closeUnauthorized:
		if _, terr := s.Tokens(ctx, true); terr != nil {
			return terr
		}
	}
	return err
}

// session is one connection: auth, snapshot, then frames until it breaks.
func (s *Stream) session(ctx context.Context) error {
	token, err := s.Tokens(ctx, false)
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, err := s.dial(dctx)
	cancel()
	if err != nil {
		return err
	}
	c.SetReadLimit(4 << 20)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	s.mu.Lock()
	s.conn, s.waits = c, map[string]chan frame{}
	s.mu.Unlock()
	s.dmu.Lock()
	s.live, s.held = false, nil
	s.dmu.Unlock()
	defer func() {
		s.mu.Lock()
		s.conn = nil
		for _, ch := range s.waits {
			close(ch)
		}
		s.waits = nil
		s.mu.Unlock()
		_ = c.CloseNow()
	}()
	readErr := make(chan error, 1)
	go func() { readErr <- s.read(sctx, c) }()

	auth, err := s.request(sctx, "auth", map[string]string{"token": token})
	if err != nil {
		// a refused token closes the socket with 4003: refresh it (a revoked session fails there and ends the stream)
		select {
		case rerr := <-readErr:
			return s.afterClose(ctx, rerr)
		case <-time.After(time.Second):
		}
		return err
	}
	if auth.OK == nil || !*auth.OK {
		// the access token was not accepted: refresh it (a revoked session fails there)
		if _, err := s.Tokens(ctx, true); err != nil {
			return err
		}
		return errors.New("ws auth refused")
	}
	snap, err := s.request(sctx, "notify.snapshot", nil)
	if err != nil {
		return err
	}
	var sn api.Snapshot
	if err := json.Unmarshal(snap.Data, &sn); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	s.dmu.Lock()
	s.H.State(StateOnline)
	s.H.Sync(sn)
	for _, f := range s.held {
		s.dispatch(f)
	}
	s.live, s.held = true, nil
	s.dmu.Unlock()
	if s.Active != nil {
		go s.reportActivity(sctx)
	}

	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	refresh := time.NewTicker(refreshEvery)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.Close(websocket.StatusNormalClosure, "bye")
			return ctx.Err()
		case err := <-readErr:
			return s.afterClose(ctx, err)
		case <-ping.C:
			if _, err := s.request(sctx, "ping", nil); err != nil {
				return err
			}
		case <-refresh.C:
			tok, err := s.Tokens(ctx, true)
			if err != nil {
				return err
			}
			if f, err := s.request(sctx, "auth.refresh", map[string]string{"token": tok}); err != nil {
				return err
			} else if f.OK == nil || !*f.OK {
				return errors.New("auth.refresh refused")
			}
		}
	}
}

// reportActivity sends notify.activity at once and then on every change, for as long as the socket lives: the CRM forgets
// it with the socket. It has its own goroutine, so a CRM slow to answer does not hold back the ping. An answer with an
// error (a CRM older than 1.2) changes nothing for the cards and is not sent again until the state changes.
func (s *Stream) reportActivity(ctx context.Context) {
	every := s.every
	if every == 0 {
		every = activityEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	var sent, reported bool
	for {
		if active, known := s.Active(); known && (!reported || active != sent) {
			sent, reported = active, true
			f, err := s.request(ctx, "notify.activity", map[string]bool{"active": active})
			if err != nil {
				s.log().Debug("stream: activity not sent", "err", err)
			} else if f.Error != nil {
				s.log().Debug("stream: activity refused", "code", f.Error.Code)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Stream) read(ctx context.Context, c *websocket.Conn) error {
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			// the waiting requests fail at once instead of their timeout
			s.mu.Lock()
			for _, ch := range s.waits {
				close(ch)
			}
			s.waits = map[string]chan frame{}
			s.mu.Unlock()
			return err
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		if f.Re != "" {
			s.mu.Lock()
			ch := s.waits[f.Re]
			delete(s.waits, f.Re)
			s.mu.Unlock()
			if ch != nil {
				ch <- f
			}
			continue
		}
		s.dmu.Lock()
		if s.live {
			s.dispatch(f)
		} else {
			s.held = append(s.held, f)
		}
		s.dmu.Unlock()
	}
}

// ContractChecker is an optional part of a Handler: the version of the contract the CRM speaks (hello).
type ContractChecker interface {
	Contract(version string)
}

func (s *Stream) dispatch(f frame) {
	switch f.Type {
	case "hello":
		var d struct {
			Contract string `json:"notify_contract"`
		}
		if c, ok := s.H.(ContractChecker); ok && json.Unmarshal(f.Data, &d) == nil {
			c.Contract(d.Contract)
		}
	case "notify.card":
		var card api.Card
		if json.Unmarshal(f.Data, &card) == nil && card.Key != "" {
			s.H.Card(card)
		}
	case "notify.clear":
		var d struct {
			Keys []string `json:"keys"`
		}
		if json.Unmarshal(f.Data, &d) == nil && len(d.Keys) > 0 {
			s.H.Clear(d.Keys)
		}
	case "notify.sync":
		var sn api.Snapshot
		if json.Unmarshal(f.Data, &sn) == nil {
			s.H.Sync(sn)
		}
	}
}

// request sends a frame and waits for its answer.
func (s *Stream) request(ctx context.Context, typ string, data any) (frame, error) {
	s.mu.Lock()
	c := s.conn
	if c == nil || s.waits == nil {
		s.mu.Unlock()
		return frame{}, errors.New("нет соединения с CRM")
	}
	id := fmt.Sprintf("w-%d", s.seq.Add(1))
	ch := make(chan frame, 1)
	s.waits[id] = ch
	s.mu.Unlock()
	f := map[string]any{"id": id, "type": typ}
	if data != nil {
		f["data"] = data
	}
	raw, _ := json.Marshal(f)
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := c.Write(wctx, websocket.MessageText, raw); err != nil {
		return frame{}, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return frame{}, errors.New("соединение закрыто")
		}
		return r, nil
	case <-wctx.Done():
		s.mu.Lock()
		delete(s.waits, id)
		s.mu.Unlock()
		return frame{}, fmt.Errorf("%s: нет ответа", typ)
	}
}

// Check asks the CRM for the current state through the live socket, without applying it: the whole way to the account
// and back works when it answers.
func (s *Stream) Check(ctx context.Context) (api.Snapshot, error) {
	var sn api.Snapshot
	f, err := s.request(ctx, "notify.snapshot", nil)
	if err != nil {
		return sn, err
	}
	if f.Error != nil {
		return sn, fmt.Errorf("%w: %s", ErrRefused, f.Error.Message)
	}
	return sn, json.Unmarshal(f.Data, &sn)
}

// Read sends «Прочитано» for a card.
func (s *Stream) Read(ctx context.Context, key string) error {
	f, err := s.request(ctx, "notify.read", map[string]string{"key": key})
	if err != nil {
		return err
	}
	if f.Error != nil {
		return fmt.Errorf("%w: %s", ErrRefused, f.Error.Message)
	}
	return nil
}
