// Package queue keeps the cards of the app. A card stays until someone removes it: «Прочитано», «Закрыть», «Скрыть
// все» or the CRM (notify.clear, notify.sync). At most Max are on the screen, the newest at the edge; the rest are counted
// by the strip and come up when a place frees. A card is replaced by its key, never duplicated (docs/APP_SPEC.md).
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

// TestFor is how long a check of the notifications stays: the only card that leaves by itself.
const TestFor = 5 * time.Second

// ListFor: the list opened from the strip closes when the mouse has been away from it this long.
const ListFor = 5 * time.Second

// StripKey stands for the strip in Hold.
const StripKey = "strip"

// KindTest is a check of the notifications: of the settings or of the CRM (contract §5.6). It proves the way, it is not
// an unread message: it is not counted and leaves for good after TestFor.
const KindTest = "test"

// KindCall is a call of a client (contract 1.4, key ring-…): it comes before the other cards and is not an unread
// message. «Звонит» (ringing) stays until the CRM clears it (an answer or the end of the call); it is not in the
// snapshot, so a snapshot or notify.sync does not drop it. «Разговор» (talking, the employee answered) stays until the
// end of the call: it is in the snapshot, so a snapshot without it drops it.
const KindCall = "call"

// CallFor is how long «Звонит» stays at most: the clear of the CRM may be lost with a broken connection. «Разговор» has
// no limit: the snapshot after reconnecting tells whether it goes on.
const CallFor = 3 * time.Minute

// MaxKept bounds the memory: beyond it the oldest cards are dropped (they stay unread in the CRM).
const MaxKept = 500

// View is what the screen shows.
type View struct {
	// Visible from the edge of the screen: Visible[0] is the closest to it (a call or the newest card).
	Visible []api.Card
	// Waiting: cards that came during «Не беспокоить» and are not shown yet.
	Waiting int
	// More: cards the server has beyond the ones sent to the app («ещё N — откройте CRM»).
	More int
	// Unread: everything unread, on the screen or not, More included.
	Unread int
	// Hidden: the unread ones that are not on the screen — the number of the strip.
	Hidden int
	// Next: when something must change by itself (a check leaves, a lost call ends, the list closes); zero — never.
	Next time.Time
	// Browsing: the person opened the list from the strip; Visible is a window of it, Offset cards from the newest.
	Browsing bool
	Offset   int
	Listed   int // cards in the list
	// Calls: ringing calls on the screen, they come first in Visible.
	Calls int
	DND   bool
}

// Manager is safe for concurrent use: the network feeds it, the screen reads it.
type Manager struct {
	mu        sync.Mutex
	cards     map[string]api.Card
	order     map[string]int64     // when the current state of a card came: the newest is at the edge
	last      int64                // the last order given
	visible   []string             // the edge of the screen first
	local     map[string]bool      // cards of the app itself: the state of the server says nothing about them
	calls     map[string]time.Time // calls and when they leave at the latest (zero — «Разговор», no limit)
	tests     map[string]time.Time // checks and when they leave (zero — not shown yet)
	dismissed map[string]time.Time // removed here: they come back only with a newer state
	held      string               // the card under the mouse
	browsing  bool                 // the list opened from the strip
	offset    int                  // the first card of the list on the screen
	browseEnd time.Time            // the list closes after the mouse has left it
	more      int
	moreSeen  int // of more, hidden by «Скрыть все»
	dnd       bool
	dndFrom   int64 // in DND only cards that came before it are shown
}

// New creates an empty manager.
func New() *Manager {
	m := &Manager{}
	m.reset()
	return m
}

func (m *Manager) reset() {
	m.cards, m.order, m.local = map[string]api.Card{}, map[string]int64{}, map[string]bool{}
	m.calls, m.tests, m.dismissed = map[string]time.Time{}, map[string]time.Time{}, map[string]time.Time{}
	m.visible, m.held, m.more, m.moreSeen, m.last = nil, "", 0, 0, 0
	m.endBrowse()
}

func (m *Manager) isCall(k string) bool { _, ok := m.calls[k]; return ok }

// talking: «Разговор» — it goes with the end of the call, a snapshot without it ends it.
func (m *Manager) talking(k string) bool { end, ok := m.calls[k]; return ok && end.IsZero() }

// markCall keeps the limit of a call: «Звонит» gets CallFor once, «Разговор» none.
func (m *Manager) markCall(c api.Card) {
	switch {
	case c.Talking():
		m.calls[c.Key] = time.Time{}
	case !m.isCall(c.Key) || m.talking(c.Key):
		m.calls[c.Key] = time.Now().Add(CallFor)
	}
}

// counted: an unread message of the CRM, not a card of the app, a check or a call.
func (m *Manager) counted(k string) bool {
	return !m.local[k] && !m.isCall(k) && m.cards[k].Kind != KindTest
}

// muted: the card came during «Не беспокоить» and waits for its end.
func (m *Manager) muted(k string) bool { return m.dnd && m.order[k] > m.dndFrom }

// settle puts on the screen the calls and then the newest cards, Max at most. It runs after every change.
func (m *Manager) settle() {
	var all []string
	for k := range m.cards {
		if !m.muted(k) {
			all = append(all, k)
		}
	}
	slices.SortFunc(all, func(a, b string) int {
		if ca, cb := m.isCall(a), m.isCall(b); ca != cb {
			if ca {
				return -1
			}
			return 1
		}
		return int(m.order[b] - m.order[a])
	})
	m.visible = all[:min(Max, len(all))]
	now := time.Now()
	for _, k := range m.visible {
		if at, test := m.tests[k]; test && at.IsZero() {
			m.tests[k] = now.Add(TestFor) // a check counts its time from the moment it is seen
		}
	}
	if m.browsing {
		list := m.list(time.Time{})
		if len(list) == 0 {
			m.endBrowse()
		} else {
			m.offset = max(min(m.offset, len(list)-m.page()), 0)
		}
	}
}

// put adds or replaces a card; a newer state (a later time or more messages) brings it to the edge.
func (m *Manager) put(c api.Card) {
	old, had := m.cards[c.Key]
	m.cards[c.Key] = c
	if !had || c.At.After(old.At) || c.Count > old.Count {
		m.last++
		m.order[c.Key] = m.last
	}
	if c.Kind == KindTest {
		if _, ok := m.tests[c.Key]; !ok {
			m.tests[c.Key] = time.Time{}
		}
	}
	for len(m.cards) > MaxKept {
		oldest, at := "", int64(0)
		for k, o := range m.order {
			if m.counted(k) && (oldest == "" || o < at) {
				oldest, at = k, o
			}
		}
		if oldest == "" {
			return
		}
		m.drop(oldest)
	}
}

// Card is a live event (notify.card): a new card or a newer state of one. Returns the sound to play ("" — none):
// a new card or more unread in it; nothing in DND.
func (m *Manager) Card(c api.Card) (sound string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if at, gone := m.dismissed[c.Key]; gone && c.Kind == KindCall && !c.At.After(at) {
		return "" // «Пропустить»: a repeat of the same call does not come back
	}
	delete(m.dismissed, c.Key) // a new message brings a dismissed card back
	old, had := m.cards[c.Key]
	if c.Kind == KindCall {
		m.markCall(c)
		if m.browsing {
			m.endBrowse() // the call must be seen: the list closes
		}
	}
	m.put(c)
	m.settle()
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
	m.settle()
}

func soundOf(c api.Card) string {
	if c.Sound == api.SoundClient {
		return api.SoundClient
	}
	return api.SoundOther
}

// Clear drops cards (read elsewhere, the conversation left the queue, the call ended).
func (m *Manager) Clear(keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		m.drop(k)
	}
	m.settle()
}

func (m *Manager) drop(k string) {
	delete(m.cards, k)
	delete(m.order, k)
	delete(m.local, k)
	delete(m.calls, k)
	delete(m.tests, k)
	if m.held == k {
		m.held = ""
		m.browseEnd = time.Now().Add(ListFor) // the next card comes under the mouse
	}
	m.visible = slices.DeleteFunc(m.visible, func(v string) bool { return v == k })
}

// Tick ends what ends by itself: a check, a call whose end was lost, the list the mouse has left.
func (m *Manager) Tick(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, end := range m.calls {
		if !end.IsZero() && !now.Before(end) {
			m.drop(k)
		}
	}
	for k, end := range m.tests {
		if !end.IsZero() && k != m.held && !now.Before(end) {
			m.drop(k)
		}
	}
	if m.browsing && m.held == "" && !now.Before(m.browseEnd) {
		m.endBrowse()
	}
	m.settle()
}

// Hold keeps a check or the list while the mouse is over it; they get their full time after. It reports a change.
func (m *Manager) Hold(key string, on bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on {
		if _, test := m.tests[key]; key == m.held || (!test && !m.browsing) {
			return false
		}
	} else if key != m.held {
		return false
	}
	if _, test := m.tests[m.held]; test {
		m.tests[m.held] = time.Now().Add(TestFor)
	}
	m.browseEnd = time.Now().Add(ListFor)
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
		if !present[k] && !m.local[k] && (!m.isCall(k) || m.talking(k)) {
			m.drop(k)
		}
	}
	for k := range m.dismissed {
		if !present[k] {
			delete(m.dismissed, k) // read or gone: forget the dismissal
		}
	}
	cards := slices.Clone(s.Cards)
	slices.SortStableFunc(cards, func(a, b api.Card) int { return a.At.Compare(b.At) }) // the newest gets the edge
	for _, c := range cards {
		if at, ok := m.dismissed[c.Key]; ok && !c.At.After(at) {
			continue
		}
		delete(m.dismissed, c.Key)
		if c.Kind == KindCall {
			m.markCall(c)
		}
		m.put(c)
	}
	m.more = max(s.Total-len(s.Cards), 0)
	m.moreSeen = min(m.moreSeen, m.more) // read in the CRM: what is left of the hidden ones
	m.settle()
}

// Dismiss removes a card here only («Закрыть»; «Прочитано» before the server answers); it comes back with a newer
// message.
func (m *Manager) Dismiss(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dismiss(key)
	m.settle()
}

func (m *Manager) dismiss(key string) {
	if c, ok := m.cards[key]; ok {
		m.dismissed[key] = c.At
	}
	m.drop(key)
}

// HideAll is «Скрыть все»: every unread card leaves this computer, the strip too; each comes back with a newer message.
// Calls and the cards of the app stay: they are not messages.
func (m *Manager) HideAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.cards {
		if m.counted(k) {
			m.dismiss(k)
		}
	}
	m.moreSeen = m.more
	m.endBrowse()
	m.settle()
}

// SetDND switches «Не беспокоить»: the cards on the screen stay, new ones and sounds wait for its end.
func (m *Manager) SetDND(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if on && !m.dnd {
		m.dndFrom = m.last
	}
	m.dnd = on
	m.settle()
}

// page is how many cards of the list fit: a call that was ringing when the list opened keeps its place.
func (m *Manager) page() int {
	n := Max
	for _, k := range m.visible {
		if m.isCall(k) {
			n--
		}
	}
	return max(n, 0)
}

// list is what the strip counts, the newest first; with a time — only the cards not newer than it.
func (m *Manager) list(before time.Time) []string {
	var out []string
	for k, c := range m.cards {
		if m.counted(k) && (before.IsZero() || !c.At.After(before)) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if c := int(m.order[b] - m.order[a]); c != 0 {
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

// Browse opens the list from the strip.
func (m *Manager) Browse() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.list(time.Time{})) == 0 {
		return
	}
	m.browsing, m.offset, m.held = true, 0, ""
	m.browseEnd = time.Now().Add(ListFor)
}

// EndBrowse closes the list: the screen shows the newest cards again.
func (m *Manager) EndBrowse() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.endBrowse()
	m.settle()
}

func (m *Manager) endBrowse() { m.browsing, m.offset, m.held = false, 0, "" }

// Scroll moves the list by cards: above zero towards the older ones.
func (m *Manager) Scroll(by int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.browsing {
		m.offset = max(m.offset+by, 0)
		m.browseEnd = time.Now().Add(ListFor)
		m.settle()
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
	m.reset()
}

// View returns what to show.
func (m *Manager) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := View{More: m.more - m.moreSeen, DND: m.dnd, Browsing: m.browsing, Offset: m.offset}
	shown := 0
	if m.browsing {
		list := m.list(time.Time{})
		v.Listed = len(list)
		for _, k := range m.visible {
			if m.isCall(k) {
				v.Visible = append(v.Visible, m.cards[k])
				v.Calls++
			}
		}
		for _, k := range list[m.offset:min(m.offset+m.page(), len(list))] {
			v.Visible = append(v.Visible, m.cards[k])
		}
	} else {
		for _, k := range m.visible {
			v.Visible = append(v.Visible, m.cards[k])
			if m.isCall(k) {
				v.Calls++
			} else if m.counted(k) {
				shown++
			}
		}
	}
	for k := range m.cards {
		if m.counted(k) {
			v.Unread++
		}
		if m.muted(k) {
			v.Waiting++
		}
	}
	v.Unread += v.More
	v.Hidden = v.Unread - shown
	if m.browsing {
		v.Hidden = v.Unread
	}
	next := func(at time.Time) {
		if !at.IsZero() && (v.Next.IsZero() || at.Before(v.Next)) {
			v.Next = at
		}
	}
	for k, at := range m.tests {
		if k != m.held {
			next(at)
		}
	}
	for _, at := range m.calls {
		if !at.IsZero() {
			next(at)
		}
	}
	if m.browsing && m.held == "" {
		next(m.browseEnd)
	}
	return v
}
