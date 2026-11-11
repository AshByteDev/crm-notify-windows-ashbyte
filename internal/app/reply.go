package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stream"
)

// An answer from a card (contract 1.10 §5.8): one at a time. While it is written the card stays on the screen
// (queue.Pin). Files are uploaded when chosen; notify.reply goes when all of them are there. A lost connection keeps the
// answer and sends it again after the next snapshot with the same client_id: the CRM makes one message of it. The text
// and the names of the files are never logged.

// MaxReplyText is the limit of the CRM for the text of an answer.
const MaxReplyText = 10000

// uploadFor bounds the upload of one file.
const uploadFor = 10 * time.Minute

// File is a file of an answer.
type File struct {
	Name      string
	Size      int64
	ID        string // from POST /uploads; "" — not there yet
	Uploading bool
	Error     string // the upload failed: the words of the CRM or of the system
	seq       int    // the upload finds its file by it
}

// Draft is the answer being written, as the screen shows it.
type Draft struct {
	Key   string
	Mode  string // the id of the chosen mode of card.Reply
	Files []File
	// Status: the words under the field (an error when Failed).
	Status string
	Failed bool
	// Sending: the answer has been handed over and waits for the files, the connection or the CRM; the text is fixed.
	Sending bool
	// Busy: notify.reply is on its way now — it cannot be called back.
	Busy bool
}

type draft struct {
	Draft
	text     string
	clientID string
	seq      int
}

// Reply is the answer being written; false — none.
func (a *App) Reply() (Draft, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.draft == nil {
		return Draft{}, false
	}
	d := a.draft.Draft
	d.Files = slices.Clone(d.Files)
	return d, true
}

// StartReply opens an answer in a card that allows it; the answer of another card is given up.
func (a *App) StartReply(key string) bool {
	c, ok := a.Q.Get(key)
	if !ok || c.Reply == nil || len(c.Reply.Modes) == 0 {
		return false
	}
	a.mu.Lock()
	if a.draft != nil && a.draft.Key == key {
		a.mu.Unlock()
		return true
	}
	if a.draft != nil && a.draft.Busy {
		a.mu.Unlock()
		return false
	}
	a.draft = &draft{Draft: Draft{Key: key, Mode: c.Reply.Modes[0].ID}}
	a.mu.Unlock()
	a.Q.Pin(key)
	a.changed()
	return true
}

// CancelReply gives the answer up (the card stays); false — it is on its way and cannot be called back.
func (a *App) CancelReply() bool {
	a.mu.Lock()
	if a.draft == nil {
		a.mu.Unlock()
		return true
	}
	if a.draft.Busy {
		a.mu.Unlock()
		return false
	}
	a.draft = nil
	a.mu.Unlock()
	a.Q.Unpin()
	a.changed()
	return true
}

// replyBusy: the answer of the card is on its way: the card cannot be read or closed now.
func (a *App) replyBusy(key string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.draft != nil && a.draft.Key == key && a.draft.Busy
}

// dropReply ends the answer of a card that is read or closed.
func (a *App) dropReply(key string) {
	a.mu.Lock()
	mine := a.draft != nil && a.draft.Key == key && !a.draft.Busy
	if mine {
		a.draft = nil
	}
	a.mu.Unlock()
	if mine {
		a.Q.Unpin()
	}
}

// SetReplyMode chooses where the answer goes (a mode of the card).
func (a *App) SetReplyMode(id string) {
	a.mu.Lock()
	d := a.draft
	a.mu.Unlock()
	if d == nil {
		return
	}
	c, ok := a.Q.Get(d.Key)
	if !ok || c.Reply == nil || !slices.ContainsFunc(c.Reply.Modes, func(m api.Mode) bool { return m.ID == id }) {
		return
	}
	a.mu.Lock()
	if a.draft == d && !d.Sending {
		d.Mode = id
	}
	a.mu.Unlock()
	a.changed()
}

// Attach adds a file to the answer and uploads it at once. The limits of the card are checked first: a file beyond
// them is not uploaded, the reason is shown.
func (a *App) Attach(path string) {
	a.mu.Lock()
	d := a.draft
	a.mu.Unlock()
	if d == nil {
		return
	}
	c, _ := a.Q.Get(d.Key)
	if c.Reply == nil || c.Reply.Files == nil {
		return
	}
	lim := *c.Reply.Files
	name := filepath.Base(path)
	st, err := os.Stat(path)
	why := ""
	switch {
	case err != nil:
		why = "Не удалось открыть файл «" + name + "»"
	case st.IsDir():
		why = "Папку приложить нельзя: «" + name + "»"
	case lim.MaxMB > 0 && st.Size() > int64(lim.MaxMB)<<20:
		why = fmt.Sprintf("Файл «%s» больше %d МБ", name, lim.MaxMB)
	case st.Size() == 0:
		why = "Файл «" + name + "» пустой"
	}
	a.mu.Lock()
	if a.draft != d || d.Sending {
		a.mu.Unlock()
		return
	}
	if why == "" && lim.MaxCount > 0 && len(d.Files) >= lim.MaxCount {
		why = fmt.Sprintf("Не больше %d %s", lim.MaxCount, plural(lim.MaxCount, "файла", "файлов", "файлов"))
	}
	if why != "" {
		d.Status, d.Failed = why, true
		a.mu.Unlock()
		a.changed()
		return
	}
	d.seq++
	d.Files = append(d.Files, File{Name: name, Size: st.Size(), Uploading: true, seq: d.seq})
	d.Status, d.Failed = "", false
	a.mu.Unlock()
	a.changed()
	go a.upload(d, path, d.Files[len(d.Files)-1].seq)
}

func plural(n int, one, few, many string) string {
	n %= 100
	if n >= 11 && n <= 14 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

func (a *App) upload(d *draft, path string, seq int) {
	ctx, cancel := context.WithTimeout(context.Background(), uploadFor)
	defer cancel()
	var at api.Attachment
	err := func() error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		tok, err := a.token(ctx, false)
		if err != nil {
			return err
		}
		at, err = a.api.Upload(ctx, tok, filepath.Base(path), f)
		return err
	}()
	if err != nil {
		a.log().Warn("reply: upload failed", "err", uploadError(err)) // no name of the file
	}
	a.mu.Lock()
	i := slices.IndexFunc(d.Files, func(f File) bool { return f.seq == seq })
	if a.draft != d || i < 0 {
		a.mu.Unlock()
		return // given up meanwhile; the CRM deletes the file itself
	}
	f := &d.Files[i]
	f.Uploading = false
	if err != nil {
		f.Error = uploadError(err)
	} else {
		f.ID = at.ID
	}
	send := d.Sending
	a.mu.Unlock()
	a.changed()
	if send {
		a.trySend()
	}
}

// uploadError is the reason for the person: the words of the CRM when it gave them.
func uploadError(err error) string {
	var ae *api.Error
	if errors.As(err, &ae) {
		if ae.Message != "" {
			return ae.Message
		}
		return fmt.Sprintf("CRM не приняла файл (%d)", ae.Status)
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return "файл не открылся"
	}
	return "нет связи с CRM"
}

// RemoveFile takes a file out of the answer (uploaded already — the CRM deletes it itself).
func (a *App) RemoveFile(i int) {
	a.mu.Lock()
	d := a.draft
	if d == nil || d.Sending || i < 0 || i >= len(d.Files) {
		a.mu.Unlock()
		return
	}
	d.Files = slices.Delete(d.Files, i, i+1)
	d.Status, d.Failed = "", false
	a.mu.Unlock()
	a.changed()
}

// SendReply hands the answer over: it goes when the files are uploaded and the CRM is reachable.
func (a *App) SendReply(text string) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	a.mu.Lock()
	d := a.draft
	if d == nil || d.Sending {
		a.mu.Unlock()
		return
	}
	why := ""
	switch {
	case len([]rune(text)) > MaxReplyText:
		why = fmt.Sprintf("Текст длиннее %d знаков", MaxReplyText)
	case slices.ContainsFunc(d.Files, func(f File) bool { return f.Error != "" }):
		why = "Уберите файл, который не загрузился"
	case text == "" && len(d.Files) == 0:
		why = "Напишите ответ или приложите файл"
	}
	if why != "" {
		d.Status, d.Failed = why, true
		a.mu.Unlock()
		a.changed()
		return
	}
	d.text, d.Sending, d.Failed, d.Status = text, true, false, ""
	if d.clientID == "" {
		d.clientID = newUUID()
	}
	a.mu.Unlock()
	a.trySend()
}

// trySend sends the answer when nothing holds it: the files are uploaded, the stream is there, nothing is on its way.
func (a *App) trySend() {
	a.mu.Lock()
	d, s := a.draft, a.stream
	if d == nil || !d.Sending || d.Busy {
		a.mu.Unlock()
		return
	}
	if slices.ContainsFunc(d.Files, func(f File) bool { return f.Uploading }) {
		d.Status, d.Failed = "Загружаю файлы, ответ уйдёт следом…", false
		a.mu.Unlock()
		a.changed()
		return
	}
	if slices.ContainsFunc(d.Files, func(f File) bool { return f.Error != "" }) {
		d.Sending, d.Status, d.Failed = false, "Уберите файл, который не загрузился", true
		a.mu.Unlock()
		a.changed()
		return
	}
	if s == nil || a.state != Online {
		d.Status, d.Failed = "Нет связи с CRM — ответ уйдёт после подключения", false
		a.mu.Unlock()
		a.changed()
		return
	}
	r := stream.ReplyRequest{Key: d.Key, Mode: d.Mode, Text: d.text, ClientID: d.clientID}
	for _, f := range d.Files {
		r.Files = append(r.Files, f.ID)
	}
	d.Busy, d.Status, d.Failed = true, "Отправляю…", false
	a.mu.Unlock()
	a.changed()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := s.Reply(ctx, r)
		cancel()
		a.replied(d, err)
	}()
}

// replied is the answer of the CRM to notify.reply.
func (a *App) replied(d *draft, err error) {
	var no *stream.Refusal
	a.mu.Lock()
	if a.draft != d {
		a.mu.Unlock()
		return
	}
	d.Busy = false
	switch {
	case err == nil:
		a.draft = nil
		a.mu.Unlock()
		a.log().Info("reply: sent", "key", d.Key, "mode", d.Mode, "files", len(d.Files))
		a.Q.Unpin()
		a.Q.Dismiss(d.Key) // the CRM reads the chat and clears the card everywhere
		a.changed()
		return
	case errors.As(err, &no):
		// nothing was sent: the text and the files stay, the next try is a new request
		d.Sending, d.Failed, d.clientID = false, true, ""
		d.Status = no.Message
		if d.Status == "" {
			d.Status = "CRM не приняла ответ"
		}
		a.log().Warn("reply: refused", "key", d.Key, "code", no.Code)
	default:
		// it may have arrived: the same client_id after the next snapshot
		d.Status, d.Failed = "Нет связи с CRM — ответ уйдёт после подключения", false
		a.log().Warn("reply: not sent", "key", d.Key, "err", err)
	}
	a.mu.Unlock()
	a.changed()
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
