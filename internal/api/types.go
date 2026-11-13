// Package api is the part of the AshCRM API the app uses (crm-ashbyte/docs/WINDOWS_NOTIFICATIONS_CRM_SPEC.md).
package api

import (
	"slices"
	"time"
)

// Card is one notification card: one per chat, task, meeting, queued conversation (key).
type Card struct {
	Key     string    `json:"key"`
	Kind    string    `json:"kind"` // dm groups mine queue mentions tasks calendar alerts test call
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	URL     string    `json:"url"` // a path in the CRM ("/team/<chat>")
	Count   int       `json:"count"`
	Sound   string    `json:"sound"` // client | delivery | other; unknown — as other
	At      time.Time `json:"at"`
	Mention bool      `json:"mention"`
	// Call: the state of a call of the PBX (kind call, contract 1.4 §5.3); Actions: the buttons of the card as the CRM
	// gives them.
	Call    *CallState `json:"call,omitempty"`
	Actions []Action   `json:"actions,omitempty"`
	// From: in a conversation with a client (contract 1.10) — who wrote last: FromClient or FromStaff (a note inside the
	// conversation the client does not see); "" elsewhere.
	From string `json:"from,omitempty"`
	// Reply: the card can be answered from here now (contract 1.10 §5.8); nil — no «Ответить».
	Reply *Reply `json:"reply,omitempty"`
	// Sticker: the last message is a sticker of the sets of the company (contract 1.11); no Hash — no picture, Body.
	Sticker *Sticker `json:"sticker,omitempty"`
	// Alert: a card to be noticed (contract 1.12: a ping of the dispatcher, «Не удалось», «Срок прошёл», an urgent
	// request): red, louder, not pushed off the screen by ordinary cards.
	Alert bool `json:"alert,omitempty"`
	// Category: where the card comes from (contract 1.13 §5.3.1) — its icon and colour; "" or unknown — by Kind.
	Category string `json:"category,omitempty"`
	// CallKind: the event is a telephone call of this kind (contract 1.14 §5.3.2) — a handset before Body: in, out,
	// missed, dropped, spam, ringing; "" — not a call, unknown — a grey handset.
	CallKind string `json:"call_kind,omitempty"`
}

// Sticker is the picture of a sticker: Code "<pack>/<id>", Hash the SHA-256 of the PNG at URL (a path of the portal).
type Sticker struct {
	Code string `json:"code"`
	Hash string `json:"hash,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Who wrote in a conversation with a client.
const (
	FromClient = "client"
	FromStaff  = "staff"
)

// Reply is how a card can be answered: Modes[0] is the default; Takes — the answer takes the conversation of the queue;
// Files — files may go with it (nil — none).
type Reply struct {
	Modes []Mode      `json:"modes"`
	Takes bool        `json:"takes,omitempty"`
	Files *FileLimits `json:"files,omitempty"`
}

// Mode is where an answer goes: client (to the client in the channel of the title), internal (a note inside the
// conversation), chat (to the chat); an unknown one is shown by its Title.
type Mode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// FileLimits of the CRM for the files of an answer.
type FileLimits struct {
	MaxMB    int `json:"max_mb"`
	MaxCount int `json:"max_count"`
}

// Attachment is a file uploaded for an answer (POST /uploads): its ID goes into notify.reply.
type Attachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

func sameReply(a, b *Reply) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Takes != b.Takes || !slices.Equal(a.Modes, b.Modes) || (a.Files == nil) != (b.Files == nil) {
		return false
	}
	return a.Files == nil || *a.Files == *b.Files
}

func sameSticker(a, b *Sticker) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// CallState is ringing (it rings at this employee) or talking (they answered, until the end of the call).
type CallState struct {
	State string `json:"state"`
}

// Call states.
const (
	CallRinging = "ringing"
	CallTalking = "talking"
)

// Talking: the employee is talking in this call — the card «Разговор» with its buttons.
func (c Card) Talking() bool { return c.Call != nil && c.Call.State == CallTalking }

// Action is a button of a card: Title on it, URL a path in the CRM (the app opens only <CRM> + URL, contract §6).
// Primary: the main of the buttons «Написать» (id message), the others go into the menu next to it.
type Action struct {
	ID      string `json:"id"` // client | message | check
	Title   string `json:"title"`
	URL     string `json:"url"`
	Primary bool   `json:"primary,omitempty"`
}

// Same reports whether two cards look the same (Card has slices: not comparable with ==).
func Same(a, b Card) bool {
	if a.Key != b.Key || a.Kind != b.Kind || a.Title != b.Title || a.Body != b.Body || a.URL != b.URL || a.Count != b.Count ||
		a.Sound != b.Sound || !a.At.Equal(b.At) || a.Mention != b.Mention || a.Talking() != b.Talking() || len(a.Actions) != len(b.Actions) ||
		a.From != b.From || !sameReply(a.Reply, b.Reply) || !sameSticker(a.Sticker, b.Sticker) || a.Alert != b.Alert || a.Category != b.Category ||
		a.CallKind != b.CallKind {
		return false
	}
	for i := range a.Actions {
		if a.Actions[i] != b.Actions[i] {
			return false
		}
	}
	return true
}

// Snapshot is the newest cards (≤ 20) and the number of all of them.
type Snapshot struct {
	Cards []Card `json:"cards"`
	Total int    `json:"total"`
}

// Sound classes.
const (
	SoundClient   = "client"
	SoundDelivery = "delivery" // contract 1.12: everything of «Доставка»
	SoundOther    = "other"
	// SoundAlert is the sound of the app for a card with Alert: the sound of the delivery, louder and twice.
	SoundAlert = "alert"
)

// Tokens of the desktop session.
type Tokens struct {
	AccessToken      string    `json:"access_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	UserID           string    `json:"user_id"`
	SessionID        string    `json:"session_id"`
}

// Me is the signed-in employee (GET /me).
type Me struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	TenantName  string `json:"tenant_name"`
}
