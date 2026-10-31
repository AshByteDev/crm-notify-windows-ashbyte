// Package queue keeps the cards of the app: at most Max on the screen (the newest at the edge), each for Show, the rest
// wait in memory; a card that has been shown stays unread until the CRM clears it. A card is replaced by its key, never
// duplicated (docs/APP_SPEC.md §3.3–3.5).
package queue

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// Max is the number of cards on the screen at once (decision of the owner).
const Max = 3

// Show is how long a card stays on the screen by default (config.json changes it). Cards of the app that need an action
// have no limit; a card under the mouse waits.
const Show = 5 * time.Second

// StripKey stands for the strip in Hold.
const StripKey = "strip"

// KindTest is a check of the notifications: of the settings or of the CRM (contract §5.6). It proves the way, it is not
// an unread message: it is not counted and leaves for good when its time ends.
const KindTest = "test"

// MaxKept bounds the memory: beyond it the oldest waiting cards are dropped (they stay unread in the CRM).
const MaxKept = 500

// View is what the screen shows.
type View struct {
	// Visible from the edge of the screen: Visible[0] is the closest to it (the newest shown).
	Visible []api.Card
	// Waiting: received cards that wait for a free place.
	Waiting int
	// More: cards the server has beyond the ones sent to the app («ещё N — откройте CRM»).
	More int
	// Unread: everything unread, on the screen or not, More included.
	Unread int
	// Next: when the next card leaves the screen (zero — none is timed).
	Next time.Time
	// Browsing: the person opened the list from the strip; Visible is a window of it, Offset cards from the newest.
	Browsing bool
	Offset   int
	Listed   int // cards in the list
	DND      bool
}

// Manager is safe for concurrent use: the network feeds it, the screen reads it.
type Manager struct {
	mu        sync.Mutex
	cards     map[string]api.Card
	visible   []string             // the edge of the screen first
	waiting   []string             // first come, first shown
	seen      []string             // shown and gone from the screen, still unread; the oldest first
	until     map[string]time.Time // when a visible card leaves the screen
	held      string               // the card under the mouse
	show      time.Duration        // how long a card stays on the screen
	browsing  bool                 // the list opened from the strip
	offset    int                  // the first card of the list on the screen
	browseEnd time.Time            // the list closes after the mouse has left it
	dismissed map[string]time.Time
	local     map[string]bool // cards of the app itself: the state of the server says nothing about them
	more      int
	dnd       bool
}

// New creates an empty manager.
func New() *Manager {
	return &Manager{cards: map[string]api.Card{}, dismissed: map[string]time.Time{}, local: map[string]bool{}, until: map[string]time.Time{},
		show: Show}
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
	if m.browsing {
		list := m.list(time.Time{})
		if len(list) == 0 {
			m.endBrowse()
		} else {
			m.offset = max(min(m.offset, len(list)-Max), 0)
			return
		}
	}
	n := min(Max-len(m.visible), len(m.waiting))
	if m.dnd || n <= 0 {
		return
	}
	chosen := m.waiting[:n]
	for i := n - 1; i >= 0; i-- {
		m.visible = append([]string{chosen[i]}, m.visible...)
		if m.timed(chosen[i]) {
			m.until[chosen[i]] = time.Now().Add(m.show)
		}
	}
	m.waiting = append([]string(nil), m.waiting[n:]...)
}

// timed: the card leaves the screen by itself (the cards of the app stay until an action, except the check).
func (m *Manager) timed(k string) bool { return !m.local[k] || m.cards[k].Kind == KindTest }

// put adds or replaces a card; a newer state shows it again. Waiting cards are kept newest first.
func (m *Manager) put(c api.Card) {
	old, had := m.cards[c.Key]
	m.cards[c.Key] = c
	if had {
		if !c.At.After(old.At) && c.Count <= old.Count {
			return
		}
		if _, on := m.until[c.Key]; on {
			m.until[c.Key] = time.Now().Add(m.show)
			return
		}
		var back bool
		if m.seen, back = remove(m.seen, c.Key); !back {
			return
		}
	}
	i := 0
	for i < len(m.waiting) && !m.cards[m.waiting[i]].At.Before(c.At) {
		i++
	}
	m.waiting = append(m.waiting[:i], append([]string{c.Key}, m.waiting[i:]...)...)
	for len(m.cards) > MaxKept {
		switch {
		case len(m.seen) > 0:
			m.drop(m.seen[0])
		case len(m.waiting) > 0:
			m.drop(m.waiting[len(m.waiting)-1])
		default:
			return
		}
	}
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

// Local adds a card of the app itself («Доступна версия…»): Sync leaves it alone, only an action of the person ends it.
func (m *Manager) Local(c api.Card) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.dismissed, c.Key)
	m.local[c.Key] = true
	m.put(c)
	m.fill()
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
	delete(m.local, k)
	delete(m.until, k)
	if m.held == k {
		m.held = ""
		m.browseEnd = time.Now().Add(m.show) // the next card comes under the mouse
	}
	m.visible, _ = remove(m.visible, k)
	m.waiting, _ = remove(m.waiting, k)
	m.seen, _ = remove(m.seen, k)
}

// Tick takes the cards whose time has ended off the screen; the next ones take the free places.
func (m *Manager) Tick(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browsing {
		if m.held == "" && !now.Before(m.browseEnd) {
			m.endBrowse()
			m.fill()
		}
		return
	}
	for _, k := range append([]string(nil), m.visible...) {
		at, timed := m.until[k]
		if !timed || k == m.held || now.Before(at) {
			continue
		}
		if m.cards[k].Kind == KindTest {
			m.drop(k)
			continue
		}
		delete(m.until, k)
		m.visible, _ = remove(m.visible, k)
		m.seen = append(m.seen, k)
	}
	m.fill()
}

// Hold keeps a card on the screen while the mouse is over it; the card gets its full time after. It reports a change.
func (m *Manager) Hold(key string, on bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		if _, timed := m.until[key]; key == m.held || (!timed && !m.browsing) {
			return false
		}
	} else if key != m.held {
		return false
	}
	if _, timed := m.until[m.held]; timed {
		m.until[m.held] = time.Now().Add(m.show)
	}
	m.browseEnd = time.Now().Add(m.show)
	m.held = ""
	if on {
		m.held = key
	}
	return true
}

// Sync applies the current state of the server (snapshot after connecting, notify.sync after reading): cards that
// are gone are dropped, the rest updated, unknown ones added (a card dismissed here comes back only when newer).
// It is silent: a sound comes only with notify.card (contract §5.5).
func (m *Manager) Sync(s api.Snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	present := map[string]bool{}
	for _, c := range s.Cards {
		present[c.Key] = true
	}
	for k := range m.cards {
		if !present[k] && !m.local[k] {
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
		m.put(c)
	}
	m.more = s.Total - len(s.Cards)
	if m.more < 0 {
		m.more = 0
	}
	m.fill()
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

// SetShow changes how long a card stays on the screen (nothing or less — the default).
func (m *Manager) SetShow(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		d = Show
	}
	m.show = d
}

// list is what the strip counts, the newest first; with a time — only the cards not newer than it.
func (m *Manager) list(before time.Time) []string {
	var out []string
	for k, c := range m.cards {
		if !m.local[k] && c.Kind != KindTest && (before.IsZero() || !c.At.After(before)) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if c := m.cards[b].At.Compare(m.cards[a].At); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return out
}

// Unread lists the cards of the strip not newer than a moment.
func (m *Manager) Unread(before time.Time) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.list(before)
}

// Browse opens the list from the strip. The person is looking now: nothing waits for its turn, the timers stop.
func (m *Manager) Browse() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.list(time.Time{})) == 0 {
		return
	}
	for _, k := range append(append([]string(nil), m.visible...), m.waiting...) {
		if !m.local[k] && m.cards[k].Kind != KindTest {
			m.visible, _ = remove(m.visible, k)
			m.waiting, _ = remove(m.waiting, k)
			delete(m.until, k)
			m.seen = append(m.seen, k)
		}
	}
	m.browsing, m.offset, m.held = true, 0, ""
	m.browseEnd = time.Now().Add(m.show)
}

// EndBrowse closes the list.
func (m *Manager) EndBrowse() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endBrowse()
	m.fill()
}

func (m *Manager) endBrowse() { m.browsing, m.offset, m.held = false, 0, "" }

// Scroll moves the list by cards: above zero towards the older ones.
func (m *Manager) Scroll(by int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browsing {
		m.offset = max(m.offset+by, 0)
		m.browseEnd = time.Now().Add(m.show)
		m.fill()
	}
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
	m.cards, m.visible, m.waiting, m.seen, m.dismissed, m.local, m.until, m.held, m.more = map[string]api.Card{}, nil, nil, nil,
		map[string]time.Time{}, map[string]bool{}, map[string]time.Time{}, "", 0
	m.endBrowse()
}

// View returns what to show.
func (m *Manager) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := View{Waiting: len(m.waiting), More: m.more, DND: m.dnd, Browsing: m.browsing, Offset: m.offset}
	if m.browsing {
		list := m.list(time.Time{})
		v.Listed = len(list)
		for _, k := range list[m.offset:min(m.offset+Max, len(list))] {
			v.Visible = append(v.Visible, m.cards[k])
		}
	} else {
		for _, k := range m.visible {
			v.Visible = append(v.Visible, m.cards[k])
		}
	}
	for k, c := range m.cards {
		if !m.local[k] && c.Kind != KindTest {
			v.Unread++
		}
	}
	v.Unread += m.more
	switch {
	case m.browsing && m.held == "":
		v.Next = m.browseEnd
	case !m.browsing:
		for k, at := range m.until {
			if k != m.held && (v.Next.IsZero() || at.Before(v.Next)) {
				v.Next = at
			}
		}
	}
	return v
}
