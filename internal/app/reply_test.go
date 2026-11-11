package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stream"
)

// replyCRM takes files on /api/v1/uploads and answers on notify.reply.
type replyCRM struct {
	mu      sync.Mutex
	uploads []string // names
	replies []stream.ReplyRequest
	refuse  string // the message of a conflict
	drop    int    // how many notify.reply lose the connection before an answer
	hold    chan struct{}
}

var replyCard = api.Card{Key: "chat-1", Kind: "mine", Title: "Пётр", Body: "Когда счёт?", Count: 1, From: api.FromClient,
	At: time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC),
	Reply: &api.Reply{Modes: []api.Mode{{ID: "client", Title: "Клиенту в WhatsApp"}, {ID: "internal", Title: "Внутри беседы"}},
		Files: &api.FileLimits{MaxMB: 1, MaxCount: 2}}}

func (f *replyCRM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/uploads" {
		if f.hold != nil {
			<-f.hold
		}
		file, h, err := r.FormFile("file")
		if err != nil || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, `{"error":{"code":"validation","message":"нет файла"}}`, 422)
			return
		}
		b, _ := io.ReadAll(file)
		f.mu.Lock()
		f.uploads = append(f.uploads, h.Filename)
		id := "file-" + h.Filename
		f.mu.Unlock()
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(api.Attachment{ID: id, Name: h.Filename, Size: int64(len(b))})
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := r.Context()
	send := func(v any) { b, _ := json.Marshal(v); _ = c.Write(ctx, websocket.MessageText, b) }
	for {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var in struct {
			ID, Type string
			Data     json.RawMessage
		}
		_ = json.Unmarshal(raw, &in)
		switch in.Type {
		case "auth":
			send(map[string]any{"re": in.ID, "ok": true})
			send(map[string]any{"type": "hello", "data": map[string]any{"notify_contract": "1.10"}})
		case "notify.snapshot":
			send(map[string]any{"re": in.ID, "ok": true, "data": api.Snapshot{Cards: []api.Card{replyCard}, Total: 1}})
		case "notify.reply":
			var d stream.ReplyRequest
			_ = json.Unmarshal(in.Data, &d)
			f.mu.Lock()
			f.replies = append(f.replies, d)
			drop, refuse := f.drop > 0, f.refuse
			f.drop--
			f.mu.Unlock()
			switch {
			case drop:
				_ = c.CloseNow()
				return
			case refuse != "":
				send(map[string]any{"re": in.ID, "ok": false, "error": map[string]any{"code": "conflict", "message": refuse}})
			default:
				send(map[string]any{"re": in.ID, "ok": true, "data": map[string]any{"ok": true}})
			}
		default:
			send(map[string]any{"re": in.ID, "ok": true, "data": map[string]any{}})
		}
	}
}

func (f *replyCRM) sent() []stream.ReplyRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies)
}

func replyApp(t *testing.T, crm *replyCRM) (*App, context.Context) {
	t.Helper()
	srv := httptest.NewServer(crm)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := online(t, ctx, srv.URL)
	eventually(t, "online", func() bool { return a.View().State == Online })
	return a, ctx
}

func file(t *testing.T, name string, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func gone(a *App) bool { _, ok := a.Reply(); _, card := a.Q.Get("chat-1"); return !ok && !card }

// Text and two files: the files go first, then one notify.reply with their ids; the card leaves.
func TestReplyWithFiles(t *testing.T) {
	crm := &replyCRM{}
	a, _ := replyApp(t, crm)
	if !a.StartReply("chat-1") {
		t.Fatal("no reply")
	}
	if d, _ := a.Reply(); d.Mode != "client" {
		t.Fatalf("default mode %q", d.Mode)
	}
	a.SetReplyMode("internal")
	a.SetReplyMode("nowhere") // not a mode of the card: ignored
	a.Attach(file(t, "счёт.pdf", 100))
	a.Attach(file(t, "акт.pdf", 200))
	a.Attach(file(t, "третий.pdf", 10)) // beyond max_count
	if d, _ := a.Reply(); len(d.Files) != 2 || !d.Failed {
		t.Fatalf("draft %+v", d)
	}
	a.SendReply("  Счёт приложил\r\nспасибо  ")
	eventually(t, "sent", func() bool { return gone(a) })
	r := crm.sent()
	if len(r) != 1 || r[0].Mode != "internal" || r[0].Text != "Счёт приложил\nспасибо" || r[0].ClientID == "" ||
		!slices.Equal(r[0].Files, []string{"file-счёт.pdf", "file-акт.pdf"}) {
		t.Fatalf("replies %+v", r)
	}
}

// A file beyond max_mb is not uploaded; an empty answer is not sent.
func TestReplyLimits(t *testing.T) {
	crm := &replyCRM{}
	a, _ := replyApp(t, crm)
	a.StartReply("chat-1")
	a.Attach(file(t, "big.bin", 1<<20+1))
	d, _ := a.Reply()
	if len(d.Files) != 0 || !strings.Contains(d.Status, "больше 1 МБ") {
		t.Fatalf("draft %+v", d)
	}
	a.SendReply("   ")
	if d, _ := a.Reply(); d.Sending || !d.Failed {
		t.Fatalf("empty sent: %+v", d)
	}
	a.SendReply(strings.Repeat("я", MaxReplyText+1))
	if d, _ := a.Reply(); d.Sending {
		t.Fatal("too long sent")
	}
	if len(crm.sent()) != 0 || len(crm.uploads) != 0 {
		t.Fatal("something went to the CRM")
	}
}

// The CRM refuses (another took the conversation): its words are shown, the answer stays; the next try is a new request.
func TestReplyRefused(t *testing.T) {
	crm := &replyCRM{refuse: "Беседу уже взял Иван"}
	a, _ := replyApp(t, crm)
	a.StartReply("chat-1")
	a.SendReply("Здравствуйте")
	eventually(t, "refused", func() bool { d, _ := a.Reply(); return d.Failed && !d.Sending })
	if d, _ := a.Reply(); d.Status != "Беседу уже взял Иван" {
		t.Fatalf("status %q", d.Status)
	}
	if _, ok := a.Q.Get("chat-1"); !ok {
		t.Fatal("the card left")
	}
	crm.mu.Lock()
	crm.refuse = ""
	crm.mu.Unlock()
	a.SendReply("Здравствуйте")
	eventually(t, "sent", func() bool { return gone(a) })
	if r := crm.sent(); len(r) != 2 || r[0].ClientID == r[1].ClientID {
		t.Fatalf("replies %+v", r)
	}
}

// The connection breaks on the way: after the reconnect the same request goes again (the CRM makes one message of it).
func TestReplyRepeatedAfterReconnect(t *testing.T) {
	crm := &replyCRM{drop: 1}
	a, _ := replyApp(t, crm)
	a.StartReply("chat-1")
	a.SendReply("Здравствуйте")
	eventually(t, "sent", func() bool { return gone(a) })
	r := crm.sent()
	if len(r) != 2 || r[0].ClientID != r[1].ClientID || r[1].Text != "Здравствуйте" {
		t.Fatalf("replies %+v", r)
	}
}

// The answer waits for its file; it cannot be called back while on its way, but can while waiting.
func TestReplyWaitsForUpload(t *testing.T) {
	crm := &replyCRM{hold: make(chan struct{})}
	a, _ := replyApp(t, crm)
	a.StartReply("chat-1")
	a.Attach(file(t, "a.txt", 5))
	a.SendReply("")
	time.Sleep(100 * time.Millisecond)
	if d, _ := a.Reply(); !d.Sending || d.Busy || len(crm.sent()) != 0 {
		t.Fatalf("draft %+v", d)
	}
	close(crm.hold)
	eventually(t, "sent", func() bool { return gone(a) })
	if r := crm.sent(); len(r) != 1 || r[0].Text != "" || len(r[0].Files) != 1 {
		t.Fatalf("replies %+v", r)
	}
}

// «Закрыть» gives the answer up; a card without reply cannot be answered.
func TestReplyClosed(t *testing.T) {
	crm := &replyCRM{}
	a, _ := replyApp(t, crm)
	a.StartReply("chat-1")
	a.Close("chat-1")
	if _, ok := a.Reply(); ok {
		t.Fatal("draft stays")
	}
	a.Q.Local(api.Card{Key: TestKey, Kind: "test"})
	if a.StartReply(TestKey) {
		t.Fatal("a card without reply")
	}
}
