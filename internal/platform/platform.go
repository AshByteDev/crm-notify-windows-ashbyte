// Package platform is what differs between Windows (the product) and other systems (development, tests): where the
// settings live, how the session key is protected, the browser, autostart and sounds.
package platform

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppName is the folder of the app in the profile of the user and its name in autostart.
const AppName = "AshCRM Notify"

// Dir is the folder of the settings: %APPDATA%\AshCRM Notify on Windows.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, AppName)
	return d, os.MkdirAll(d, 0o700)
}

// Config is kept in config.json; the session key is apart, protected (Secrets).
type Config struct {
	// Server overrides the address built into the app (an owner's build serves one CRM).
	Server       string `json:"server,omitempty"`
	DND          bool   `json:"dnd"`
	CheckUpdates bool   `json:"check_updates"`
	// Position is the place of the cards on the screen (empty — the default corner, app.NormalPosition).
	Position string `json:"position,omitempty"`
	// Autostart is asked once (on the first sign-in) and then follows the menu.
	AutostartSet bool `json:"autostart_set"`
}

// LoadConfig reads config.json (defaults when missing).
func LoadConfig(dir string) Config {
	c := Config{CheckUpdates: true}
	if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return c
}

// SaveConfig writes config.json.
func SaveConfig(dir string, c Config) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "config.json"))
}

// Secret is the session of the app: the refresh token rotates on every use and is saved at once.
type Secret struct {
	RefreshToken string `json:"refresh_token"`
	UserID       string `json:"user_id"`
	SessionID    string `json:"session_id"`
}

const secretFile = "session.bin"

// SaveSecret protects the session (DPAPI of the current Windows user) and writes it.
func SaveSecret(dir string, s Secret) error {
	b, _ := json.Marshal(s)
	enc, err := protect(b)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, secretFile+".tmp")
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, secretFile))
}

// LoadSecret reads the session (ok=false — not signed in).
func LoadSecret(dir string) (Secret, bool) {
	enc, err := os.ReadFile(filepath.Join(dir, secretFile))
	if err != nil {
		return Secret{}, false
	}
	b, err := unprotect(enc)
	if err != nil {
		return Secret{}, false
	}
	var s Secret
	if json.Unmarshal(b, &s) != nil || s.RefreshToken == "" {
		return Secret{}, false
	}
	return s, true
}

// DeleteSecret forgets the session (sign-out).
func DeleteSecret(dir string) { _ = os.Remove(filepath.Join(dir, secretFile)) }
