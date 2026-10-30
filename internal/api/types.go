// Package api is the part of the AshCRM API the app uses (crm-ashbyte/docs/WINDOWS_NOTIFICATIONS_CRM_SPEC.md).
package api

import "time"

// Card is one notification card: one per chat, task, meeting, queued conversation (key).
type Card struct {
	Key     string    `json:"key"`
	Kind    string    `json:"kind"` // dm groups mine queue mentions tasks calendar alerts
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	URL     string    `json:"url"` // a path in the CRM ("/team/<chat>")
	Count   int       `json:"count"`
	Sound   string    `json:"sound"` // client | other
	At      time.Time `json:"at"`
	Mention bool      `json:"mention"`
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
