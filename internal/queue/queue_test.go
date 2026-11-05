package queue

import (
	"fmt"
	"testing"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

var t0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func card(key string, count int, min int) api.Card {
	return api.Card{Key: key, Title: key, Count: count, At: t0.Add(time.Duration(min) * time.Minute), Sound: api.SoundOther}
}

func keys(v View) string {
	s := ""
	for _, c := range v.Visible {
		s += c.Key + " "
	}
	return s
}

// later is a moment far beyond every timer of the queue but a lost call.
func later() time.Time { return time.Now().Add(time.Minute) }

func filled(n int) *Manager {
	m := New()
	for i := 1; i <= n; i++ {
		m.Card(card(fmt.Sprint("c", i), 1, i))
	}
	return m
}

// Five messages: the newest three on the screen, the strip counts two; nothing leaves by itself (acceptance 1, 5).
func TestNewestThreeStay(t *testing.T) {
	m := New()
	for i := 1; i <= 5; i++ {
		if snd := m.Card(card(fmt.Sprint("c", i), 1, i)); snd != api.SoundOther {
			t.Fatalf("card %d: sound %q", i, snd)
		}
	}
	v := m.View()
	if keys(v) != "c5 c4 c3 " || v.Hidden != 2 || v.Unread != 5 || !v.Next.IsZero() {
		t.Fatalf("%q %+v", keys(v), v)
	}
	m.Tick(time.Now().Add(10 * time.Minute))
	if keys(m.View()) != "c5 c4 c3 " {
		t.Fatal("a card left by itself")
	}
	// a place frees up → the newest of the strip comes back
	m.Dismiss("c4")
	if v := m.View(); keys(v) != "c5 c3 c2 " || v.Hidden != 1 {
		t.Fatalf("after closing: %q %d", keys(v), v.Hidden)
	}
}

// A newer message of a chat brings its card to the edge with a sound (TZ §2.4); the same count — quietly.
func TestNewerMessageRaisesCard(t *testing.T) {
	m := filled(5)
	if snd := m.Card(card("c1", 2, 9)); snd == "" {
		t.Error("one more unread: sound")
	}
	if keys(m.View()) != "c1 c5 c4 " {
		t.Fatal(keys(m.View()))
	}
	if snd := m.Card(card("c3", 1, 3)); snd != "" || keys(m.View()) != "c1 c5 c4 " {
		t.Fatalf("a repeat: %q %q", snd, keys(m.View()))
	}
	c := card("q", 0, 10)
	c.Sound = api.SoundClient
	if snd := m.Card(c); snd != api.SoundClient {
		t.Errorf("client sound: %q", snd)
	}
}

// «Закрыть» removes the card here only; the next message brings it back (acceptance 2).
func TestDismissComesBackWithNews(t *testing.T) {
	m := New()
	m.Card(card("a", 1, 1))
	m.Card(card("b", 1, 2))
	m.Dismiss("a")
	if keys(m.View()) != "b " {
		t.Fatal(keys(m.View()))
	}
	// the server still has «a» (not read) — it stays dismissed; «b» was read elsewhere (acceptance 4)
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 1, 1)}, Total: 1})
	if v := m.View(); keys(v) != "" || v.Unread != 0 {
		t.Fatalf("%q %d", keys(v), v.Unread)
	}
	if snd := m.Card(card("a", 2, 5)); snd == "" || keys(m.View()) != "a " {
		t.Fatalf("back: %q %q", snd, keys(m.View()))
	}
	m.Dismiss("a")
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 3, 6)}, Total: 1})
	if keys(m.View()) != "a " {
		t.Fatal("a newer state in a sync brings it back too")
	}
}

func TestSnapshotNewestAtTheEdge(t *testing.T) {
	m := New()
	m.Card(card("old", 1, 0))
	cards := []api.Card{}
	for _, i := range []int{3, 20, 7, 1, 15} { // any order
		cards = append(cards, card(fmt.Sprint("n", i), 1, i))
	}
	m.Sync(api.Snapshot{Cards: cards, Total: 12})
	v := m.View()
	if keys(v) != "n20 n15 n7 " || v.More != 7 || v.Hidden != 9 || v.Unread != 12 {
		t.Fatalf("%q %+v", keys(v), v)
	}
	if _, ok := m.Get("old"); ok {
		t.Error("a card the server no longer has is dropped")
	}
}

// The state of the server knows nothing about the cards of the app itself.
func TestSyncKeepsLocalCards(t *testing.T) {
	m := New()
	m.Local(card("update", 0, 1))
	m.Card(card("a", 1, 2))
	m.Sync(api.Snapshot{})
	if v := m.View(); keys(v) != "update " || v.Unread != 0 {
		t.Fatal(keys(v))
	}
	m.Dismiss("update") // «Позже»
	m.Sync(api.Snapshot{})
	if keys(m.View()) != "" {
		t.Fatal(keys(m.View()))
	}
	m.Local(card("update", 0, 3)) // the next check
	if keys(m.View()) != "update " {
		t.Fatal(keys(m.View()))
	}
}

// A card of the app pushed off by messages comes back when a place frees.
func TestLocalCardComesBack(t *testing.T) {
	m := New()
	m.Local(card("update", 0, 1))
	m.Card(card("a", 1, 2))
	m.Card(card("b", 1, 3))
	m.Card(card("c", 1, 4))
	if keys(m.View()) != "c b a " {
		t.Fatal(keys(m.View()))
	}
	m.Dismiss("b")
	if keys(m.View()) != "c a update " {
		t.Fatal(keys(m.View()))
	}
}

func TestDND(t *testing.T) {
	m := New()
	m.Card(card("before", 1, 0))
	m.SetDND(true)
	if snd := m.Card(card("a", 1, 1)); snd != "" {
		t.Error("no sound in DND")
	}
	if v := m.View(); keys(v) != "before " || v.Waiting != 1 || !v.DND || v.Unread != 2 {
		t.Fatalf("%q %+v", keys(v), v)
	}
	m.SetDND(false)
	if keys(m.View()) != "a before " {
		t.Fatal("shown after DND")
	}
	m.Reset()
	if v := m.View(); len(v.Visible) != 0 || v.Waiting != 0 || v.Unread != 0 {
		t.Fatal("reset")
	}
}

func TestMemoryIsBounded(t *testing.T) {
	m := New()
	for i := 0; i < MaxKept+50; i++ {
		m.Card(card(fmt.Sprint("k", i), 1, i))
	}
	v := m.View()
	if v.Unread != MaxKept || keys(v) != fmt.Sprintf("k%d k%d k%d ", MaxKept+49, MaxKept+48, MaxKept+47) {
		t.Fatalf("kept %d, %q", v.Unread, keys(v))
	}
	if _, ok := m.Get("k0"); ok {
		t.Fatal("the oldest goes first")
	}
}

// A check is not an unread message: neither the one of the settings nor test-<uuid> of the CRM. It is the only card
// that leaves by itself, and only after it has been seen.
func TestCheckLeavesForGood(t *testing.T) {
	m := New()
	own, crm := card("test", 0, 1), card("test-1", 0, 2)
	own.Kind, crm.Kind = KindTest, KindTest
	m.Local(own)
	m.Card(crm)
	if v := m.View(); len(v.Visible) != 2 || v.Unread != 0 || v.Hidden != 0 || v.Next.IsZero() {
		t.Fatalf("%+v", v)
	}
	if !m.Hold("test", true) {
		t.Fatal("a check is held under the mouse")
	}
	m.Tick(later())
	if keys(m.View()) != "test " {
		t.Fatal(keys(m.View()))
	}
	m.Hold("test", false)
	m.Tick(later())
	if v := m.View(); len(v.Visible) != 0 || !v.Next.IsZero() {
		t.Fatalf("%+v", v)
	}
	if _, ok := m.Get("test"); ok {
		t.Fatal("the check is forgotten")
	}
}

func TestCheckWaitsForDND(t *testing.T) {
	m := New()
	m.SetDND(true)
	c := card("test", 0, 1)
	c.Kind = KindTest
	m.Local(c)
	m.Tick(later())
	if v := m.View(); v.Waiting != 1 || !v.Next.IsZero() {
		t.Fatalf("%+v", v)
	}
	m.SetDND(false)
	if keys(m.View()) != "test " {
		t.Fatal("the check is seen after DND")
	}
}

// «Скрыть все» empties the screen and the strip here; a new message comes as usual (acceptance 6).
func TestHideAll(t *testing.T) {
	m := New()
	cards := []api.Card{}
	for i := 1; i <= 5; i++ {
		cards = append(cards, card(fmt.Sprint("c", i), 1, i))
	}
	m.Sync(api.Snapshot{Cards: cards, Total: 9})
	m.Local(card("update", 0, 0))
	m.Browse()
	m.HideAll()
	v := m.View()
	if keys(v) != "update " || v.Unread != 0 || v.Hidden != 0 || v.Browsing {
		t.Fatalf("%q %+v", keys(v), v)
	}
	m.Sync(api.Snapshot{Cards: cards, Total: 9}) // the next sync: still hidden
	if v := m.View(); v.Unread != 0 {
		t.Fatalf("%+v", v)
	}
	m.Sync(api.Snapshot{Cards: cards[:4], Total: 6}) // read in the CRM
	if v := m.View(); v.Unread != 0 {
		t.Fatalf("%+v", v)
	}
	m.Card(card("c1", 2, 9))
	if v := m.View(); keys(v) != "c1 update " || v.Unread != 1 {
		t.Fatalf("%q %+v", keys(v), v)
	}
}

// The list shows the newest three of everything unread, the wheel moves it.
func TestBrowse(t *testing.T) {
	m := filled(5)
	m.Browse()
	v := m.View()
	if keys(v) != "c5 c4 c3 " || !v.Browsing || v.Listed != 5 || !v.Next.After(time.Now()) {
		t.Fatalf("%q %+v", keys(v), v)
	}
	m.Scroll(1)
	if keys(m.View()) != "c4 c3 c2 " {
		t.Fatal(keys(m.View()))
	}
	m.Scroll(9)
	if v := m.View(); keys(v) != "c3 c2 c1 " || v.Offset != 2 {
		t.Fatal(keys(v), v.Offset)
	}
	m.Scroll(-9)
	if keys(m.View()) != "c5 c4 c3 " {
		t.Fatal(keys(m.View()))
	}
}

// The list closes when the mouse has left it; the newest cards are on the screen again.
func TestBrowseEndsWithoutMouse(t *testing.T) {
	m := filled(4)
	m.Browse()
	m.Scroll(1)
	if !m.Hold(StripKey, true) {
		t.Fatal("the strip holds the list")
	}
	if !m.View().Next.IsZero() {
		t.Fatal("no end under the mouse")
	}
	m.Tick(later())
	if !m.View().Browsing {
		t.Fatal("the mouse is there")
	}
	m.Hold(StripKey, false)
	m.Tick(later())
	if v := m.View(); v.Browsing || keys(v) != "c4 c3 c2 " || v.Unread != 4 {
		t.Fatalf("%q %+v", keys(v), v)
	}
}

// Working through the list: the card goes, the next one comes, an empty list closes.
func TestBrowseActions(t *testing.T) {
	m := filled(4)
	m.Browse()
	m.Scroll(1)
	m.Dismiss("c1")
	if v := m.View(); keys(v) != "c4 c3 c2 " || v.Offset != 0 {
		t.Fatalf("%q %d", keys(v), v.Offset)
	}
	m.Clear("c4", "c3", "c2")
	if v := m.View(); v.Browsing || v.Unread != 0 {
		t.Fatalf("%+v", v)
	}
}

// A new message during the list joins it at the top.
func TestNewCardDuringBrowse(t *testing.T) {
	m := filled(1)
	m.Browse()
	m.Card(card("new", 1, 9))
	if keys(m.View()) != "new c1 " {
		t.Fatal(keys(m.View()))
	}
	m.EndBrowse()
	if keys(m.View()) != "new c1 " {
		t.Fatal(keys(m.View()))
	}
}

// The cards of the app stay out of the list and come back after it.
func TestBrowseLeavesLocalCards(t *testing.T) {
	m := filled(1)
	m.Local(card("update", 0, 5))
	m.Browse()
	if keys(m.View()) != "c1 " {
		t.Fatal(keys(m.View()))
	}
	m.EndBrowse()
	if keys(m.View()) != "update c1 " {
		t.Fatal(keys(m.View()))
	}
}

func TestBrowseNeedsCards(t *testing.T) {
	m := New()
	m.Local(card("update", 0, 5))
	m.Browse()
	if m.View().Browsing {
		t.Fatal("nothing to list")
	}
}

func TestUnreadBefore(t *testing.T) {
	m := filled(3)
	if got := m.Unread(t0.Add(2 * time.Minute)); len(got) != 2 || got[0] != "c2" {
		t.Fatal(got)
	}
}

func TestUnreadCountsServerRest(t *testing.T) {
	m := New()
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 1, 1)}, Total: 8})
	if v := m.View(); v.Unread != 8 || v.Hidden != 7 {
		t.Fatalf("%+v", v)
	}
}

func call(key string) api.Card {
	return api.Card{Key: key, Kind: KindCall, Title: "Звонит: " + key, At: t0, Sound: api.SoundClient}
}

// A call comes at once to the edge, before newer cards; it is not unread.
func TestCallComesFirst(t *testing.T) {
	m := filled(3)
	if snd := m.Card(call("ring-1")); snd != api.SoundClient {
		t.Fatalf("sound %q", snd)
	}
	m.Card(card("c4", 1, 4))
	v := m.View()
	if keys(v) != "ring-1 c4 c3 " || v.Unread != 4 || v.Hidden != 2 || v.Calls != 1 {
		t.Fatalf("%q %+v", keys(v), v)
	}
	if snd := m.Card(call("ring-1")); snd != "" {
		t.Fatalf("the same call again: %q", snd)
	}
}

// The snapshot has no calls: a snapshot or notify.sync leaves them, notify.clear ends them.
func TestCallStaysUntilClear(t *testing.T) {
	m := New()
	m.Card(call("ring-1"))
	m.Sync(api.Snapshot{Cards: []api.Card{card("c1", 1, 1)}, Total: 1})
	if _, ok := m.Get("ring-1"); !ok {
		t.Fatal("dropped by the sync")
	}
	m.HideAll()
	if _, ok := m.Get("ring-1"); !ok {
		t.Fatal("«Скрыть все» is about messages")
	}
	m.Clear("ring-1")
	if v := m.View(); keys(v) != "" {
		t.Fatalf("%q", keys(v))
	}
}

// A lost clear does not keep a call forever.
func TestCallEndsByItself(t *testing.T) {
	m := New()
	m.Card(call("ring-1"))
	if v := m.View(); v.Next.IsZero() || time.Until(v.Next) > CallFor {
		t.Fatalf("next %v", v.Next)
	}
	m.Tick(time.Now().Add(CallFor + time.Second))
	if _, ok := m.Get("ring-1"); ok {
		t.Fatal("still ringing")
	}
}

// A call closes the list from the strip; a call on the screen keeps its place when the list opens.
func TestCallAndList(t *testing.T) {
	m := filled(5)
	m.Browse()
	m.Card(call("ring-1"))
	if v := m.View(); v.Browsing || keys(v) != "ring-1 c5 c4 " {
		t.Fatalf("browsing %v %q", v.Browsing, keys(v))
	}
	m.Browse()
	v := m.View()
	if !v.Browsing || keys(v) != "ring-1 c5 c4 " || v.Calls != 1 {
		t.Fatalf("list %q calls %d", keys(v), v.Calls)
	}
	m.Scroll(10)
	if v := m.View(); keys(v) != "ring-1 c2 c1 " {
		t.Fatalf("the end of the list %q", keys(v))
	}
}
