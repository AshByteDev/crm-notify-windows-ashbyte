// Package queue keeps the cards of the app: at most Max on the screen (the newest at the bottom), the rest wait in
// memory; a card is replaced by its key, never duplicated (docs/APP_SPEC.md §3.3–3.5).
package queue

import (
	"sync"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// Max is the number of cards on the screen at once (decision of the owner).
const Max = 3

// MaxKept bounds the memory: beyond it the oldest waiting cards are dropped (they stay unread in the CRM).
const MaxKept = 500

// View is what the screen shows.
type View struct {
	// Visible from the bottom of the screen up: Visible[0] is the lowest (the newest shown).
	Visible []api.Card
	// Waiting: received cards that wait for a free place.
	Waiting int
	// More: cards the server has beyond the ones sent to the app («ещё N — откройте CRM»).
	More int
	DND  bool
}

// Manager is safe for concurrent use: the network feeds it, the screen reads it.
type Manager struct {
	mu        sync.Mutex
	cards     map[string]api.Card
	visible   []string // bottom first
	waiting   []string // first come, first shown
	dismissed map[string]time.Time
	more      int
	dnd       bool
}

// New creates an empty manager.
func New() *Manager {
	return &Manager{cards: map[string]api.Card{}, dismissed: map[string]time.Time{}}
}

func remove(list []string, key string) ([]string, bool) {
	for i, k := range list {
		if k == key {
			return append(list[:i:i], list[i+1:]...), true
		}
	}
	return list, false
}

// fill moves waiting cards to free places (not in DND), the newest first; the newest shown ends at the bottom.
func (m *Manager) fill() {
	n := min(Max-len(m.visible), len(m.waiting))
	if m.dnd || n <= 0 {
		return
	}
	chosen := m.waiting[:n]
	for i := n - 1; i >= 0; i-- {
		m.visible = append([]string{chosen[i]}, m.visible...)
	}
	m.waiting = append([]string(nil), m.waiting[n:]...)
}

// put adds or replaces a card; isNew — the key was not there. Waiting cards are kept newest first.
func (m *Manager) put(c api.Card) (isNew bool) {
	_, had := m.cards[c.Key]
	m.cards[c.Key] = c
	if had {
		return false
	}
	i := 0
	for i < len(m.waiting) && !m.cards[m.waiting[i]].At.Before(c.At) {
		i++
	}
	m.waiting = append(m.waiting[:i], append([]string{c.Key}, m.waiting[i:]...)...)
	for len(m.cards) > MaxKept && len(m.waiting) > 0 {
		old := m.waiting[len(m.waiting)-1]
		m.waiting = m.waiting[:len(m.waiting)-1]
		delete(m.cards, old)
	}
	return true
}

// Card is a live event (notify.card): a new card or a newer state of one. Returns the sound to play ("" — none):
// a new card or more unread in it; nothing in DND.
func (m *Manager) Card(c api.Card) (sound string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.dismissed, c.Key) // a new message brings a dismissed card back
	old, had := m.cards[c.Key]
	m.put(c)
	m.fill()
	if m.dnd || (had && c.Count <= old.Count) {
		return ""
	}
	return soundOf(c)
}

func soundOf(c api.Card) string {
	if c.Sound == api.SoundClient {
		return api.SoundClient
	}
	return api.SoundOther
}

// Clear drops cards (read elsewhere, the conversation left the queue).
func (m *Manager) Clear(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		m.drop(k)
	}
	m.fill()
}

func (m *Manager) drop(k string) {
	delete(m.cards, k)
	m.visible, _ = remove(m.visible, k)
	m.waiting, _ = remove(m.waiting, k)
}

// Sync applies the current state of the server (snapshot after connecting, notify.sync after reading): cards that
// are gone are dropped, the rest updated, unknown ones added (a card dismissed here comes back only when newer).
// Returns the sound for cards that came while the app was away ("" — none).
func (m *Manager) Sync(s api.Snapshot) (sound string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	present := map[string]bool{}
	for _, c := range s.Cards {
		present[c.Key] = true
	}
	for k := range m.cards {
		if !present[k] {
			m.drop(k)
		}
	}
	for k := range m.dismissed {
		if !present[k] {
			delete(m.dismissed, k) // read or gone: forget the dismissal
		}
	}
	for _, c := range s.Cards {
		if at, ok := m.dismissed[c.Key]; ok && !c.At.After(at) {
			continue
		}
		delete(m.dismissed, c.Key)
		old, had := m.cards[c.Key]
		if m.put(c) || (had && c.Count > old.Count) {
			if sound == "" || soundOf(c) == api.SoundClient {
				sound = soundOf(c)
			}
		}
	}
	m.more = s.Total - len(s.Cards)
	if m.more < 0 {
		m.more = 0
	}
	m.fill()
	if m.dnd {
		return ""
	}
	return sound
}

// Dismiss removes a card here only (×, «Открыть», «Прочитано» before the server answers); it comes back with a newer
// message.
func (m *Manager) Dismiss(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.cards[key]; ok {
		m.dismissed[key] = c.At
	}
	m.drop(key)
	m.fill()
}

// SetDND switches «Не беспокоить»: no new cards on the screen and no sounds; received cards wait and show after.
func (m *Manager) SetDND(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dnd = on
	m.fill()
}

// Get returns a card by key.
func (m *Manager) Get(key string) (api.Card, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cards[key]
	return c, ok
}

// Reset forgets everything (sign-out).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cards, m.visible, m.waiting, m.dismissed, m.more = map[string]api.Card{}, nil, nil, map[string]time.Time{}, 0
}

// View returns what to show.
func (m *Manager) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := View{Waiting: len(m.waiting), More: m.more, DND: m.dnd}
	for _, k := range m.visible {
		v.Visible = append(v.Visible, m.cards[k])
	}
	return v
}
