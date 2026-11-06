// Package api is the part of the AshCRM API the app uses (crm-ashbyte/docs/WINDOWS_NOTIFICATIONS_CRM_SPEC.md).
package api

import "time"

// Card is one notification card: one per chat, task, meeting, queued conversation (key).
type Card struct {
	Key     string    `json:"key"`
	Kind    string    `json:"kind"` // dm groups mine queue mentions tasks calendar alerts test call
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	URL     string    `json:"url"` // a path in the CRM ("/team/<chat>")
	Count   int       `json:"count"`
	Sound   string    `json:"sound"` // client | other
	At      time.Time `json:"at"`
	Mention bool      `json:"mention"`
	// Call: the state of a call of the PBX (kind call, contract 1.4 §5.3); Actions: the buttons of the card as the CRM
	// gives them.
	Call    *CallState `json:"call,omitempty"`
	Actions []Action   `json:"actions,omitempty"`
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
		a.Sound != b.Sound || !a.At.Equal(b.At) || a.Mention != b.Mention || a.Talking() != b.Talking() || len(a.Actions) != len(b.Actions) {
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
	SoundClient = "client"
	SoundOther  = "other"
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
