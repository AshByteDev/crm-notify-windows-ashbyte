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

func TestThreeOnScreenRestWait(t *testing.T) {
	m := New()
	for i := 1; i <= 5; i++ {
		if snd := m.Card(card(fmt.Sprint("c", i), 1, i)); snd != api.SoundOther {
			t.Fatalf("card %d: sound %q", i, snd)
		}
	}
	v := m.View()
	if keys(v) != "c3 c2 c1 " || v.Waiting != 2 { // c4, c5 wait
		t.Fatalf("visible %q waiting %d", keys(v), v.Waiting)
	}
	// a place frees up → the next waiting card comes to the bottom
	m.Clear("c2")
	if v := m.View(); keys(v) != "c5 c3 c1 " || v.Waiting != 1 { // the newest waiting first
		t.Fatalf("after clear: %q %d", keys(v), v.Waiting)
	}
}

func TestSameKeyReplacesAndSoundsOnlyForMore(t *testing.T) {
	m := New()
	m.Card(card("chat-a", 1, 0))
	if snd := m.Card(card("chat-a", 2, 1)); snd == "" {
		t.Error("one more unread: sound")
	}
	if snd := m.Card(card("chat-a", 2, 2)); snd != "" {
		t.Error("the same count: no sound")
	}
	v := m.View()
	if len(v.Visible) != 1 || v.Visible[0].Count != 2 {
		t.Fatalf("%+v", v)
	}
	c := card("q", 0, 3)
	c.Sound = api.SoundClient
	if snd := m.Card(c); snd != api.SoundClient {
		t.Errorf("client sound: %q", snd)
	}
}

func TestSyncDropsReadAndKeepsDismissed(t *testing.T) {
	m := New()
	m.Card(card("a", 1, 1))
	m.Card(card("b", 1, 2))
	m.Dismiss("a")
	if keys(m.View()) != "b " {
		t.Fatal(keys(m.View()))
	}
	// the server still has «a» (not read) — it stays dismissed; «b» was read elsewhere
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 1, 1)}, Total: 1})
	if keys(m.View()) != "" {
		t.Fatal(keys(m.View()))
	}
	// a newer message in «a» brings it back
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 2, 5)}, Total: 1})
	if keys(m.View()) != "a " {
		t.Fatal(keys(m.View()))
	}
}

func TestSyncAfterReconnect(t *testing.T) {
	m := New()
	m.Card(card("old", 1, 0))
	cards := []api.Card{}
	for i := 20; i >= 1; i-- { // newest first, like the server
		cards = append(cards, card(fmt.Sprint("n", i), 1, i))
	}
	m.Sync(api.Snapshot{Cards: cards, Total: 27})
	v := m.View()
	if keys(v) != "n20 n19 n18 " || v.Waiting != 17 || v.More != 7 {
		t.Fatalf("%q waiting %d more %d", keys(v), v.Waiting, v.More)
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
	if keys(m.View()) != "update " {
		t.Fatal(keys(m.View()))
	}
	m.Dismiss("update") // «Позже»
	m.Sync(api.Snapshot{})
	if keys(m.View()) != "" {
		t.Fatal(keys(m.View()))
	}
	m.Local(card("update", 0, 3)) // the next daily check
	if keys(m.View()) != "update " {
		t.Fatal(keys(m.View()))
	}
}

func TestDND(t *testing.T) {
	m := New()
	m.SetDND(true)
	if snd := m.Card(card("a", 1, 1)); snd != "" {
		t.Error("no sound in DND")
	}
	if v := m.View(); len(v.Visible) != 0 || v.Waiting != 1 || !v.DND {
		t.Fatalf("%+v", v)
	}
	m.SetDND(false)
	if keys(m.View()) != "a " {
		t.Fatal("shown after DND")
	}
	m.Reset()
	if v := m.View(); len(v.Visible) != 0 || v.Waiting != 0 {
		t.Fatal("reset")
	}
}

func TestMemoryIsBounded(t *testing.T) {
	m := New()
	for i := 0; i < MaxKept+50; i++ {
		m.Card(card(fmt.Sprint("k", i), 1, i))
	}
	if v := m.View(); len(v.Visible)+v.Waiting != MaxKept {
		t.Fatalf("kept %d", len(v.Visible)+v.Waiting)
	}
	if _, ok := m.Get(fmt.Sprint("k", MaxKept+49)); !ok {
		t.Fatal("the newest card is kept")
	}
}

func later(n int) time.Time { return time.Now().Add(time.Duration(n) * (Show + time.Second)) }

// Cards leave the screen after Show but stay unread; the waiting ones follow in groups of three.
func TestCardsHideAndNextShow(t *testing.T) {
	m := New()
	for i := 1; i <= 5; i++ {
		m.Card(card(fmt.Sprint("c", i), 1, i))
	}
	if v := m.View(); v.Unread != 5 || v.Next.IsZero() {
		t.Fatalf("%+v", v)
	}
	m.Tick(time.Now())
	if keys(m.View()) != "c3 c2 c1 " {
		t.Fatal("the time has not ended")
	}
	m.Tick(later(1))
	if v := m.View(); keys(v) != "c5 c4 " || v.Waiting != 0 || v.Unread != 5 {
		t.Fatalf("%q %+v", keys(v), v)
	}
	m.Tick(later(2))
	if v := m.View(); keys(v) != "" || v.Unread != 5 || !v.Next.IsZero() {
		t.Fatalf("%q %+v", keys(v), v)
	}
	m.Clear("c1")
	if v := m.View(); v.Unread != 4 {
		t.Fatalf("read elsewhere: %+v", v)
	}
}

// A hidden card returns only with something new: repeated delivery and the state of the server do not show it again.
func TestHiddenCardReturnsWhenNewer(t *testing.T) {
	m := New()
	m.Card(card("a", 1, 1))
	m.Tick(later(1))
	m.Card(card("a", 1, 1))
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 1, 1)}, Total: 1})
	if v := m.View(); keys(v) != "" || v.Unread != 1 {
		t.Fatalf("%q %+v", keys(v), v)
	}
	if snd := m.Card(card("a", 2, 2)); snd == "" {
		t.Error("a new message sounds")
	}
	if keys(m.View()) != "a " {
		t.Fatal(keys(m.View()))
	}
}

// A newer message in a visible card gives it the full time again.
func TestNewerCardExtendsTime(t *testing.T) {
	m := New()
	m.Card(card("a", 1, 1))
	before := m.View().Next
	time.Sleep(5 * time.Millisecond)
	m.Card(card("a", 2, 2))
	if !m.View().Next.After(before) {
		t.Fatal("the time is not extended")
	}
}

func TestHoldKeepsCard(t *testing.T) {
	m := New()
	m.Card(card("a", 1, 1))
	m.Card(card("b", 1, 2))
	if !m.Hold("a", true) || m.Hold("a", true) {
		t.Fatal("hold reports a change once")
	}
	m.Tick(later(1))
	if keys(m.View()) != "a " {
		t.Fatal(keys(m.View()))
	}
	if !m.View().Next.IsZero() {
		t.Fatal("a held card has no end")
	}
	if m.Hold("b", false) {
		t.Fatal("not the held card")
	}
	if !m.Hold("a", false) {
		t.Fatal("release")
	}
	m.Tick(time.Now())
	if keys(m.View()) != "a " {
		t.Fatal("full time after the mouse leaves")
	}
	m.Tick(later(1))
	if keys(m.View()) != "" {
		t.Fatal(keys(m.View()))
	}
}

func TestLocalCardsStayUntilAction(t *testing.T) {
	m := New()
	m.Local(card("update", 0, 1))
	m.Tick(later(1))
	if v := m.View(); keys(v) != "update " || v.Unread != 0 || !v.Next.IsZero() {
		t.Fatalf("%q %+v", keys(v), v)
	}
}

// A check is not an unread message: neither the one of the settings nor test-<uuid> of the CRM.
func TestCheckLeavesForGood(t *testing.T) {
	m := New()
	own, crm := card("test", 0, 1), card("test-1", 0, 2)
	own.Kind, crm.Kind = KindTest, KindTest
	m.Local(own)
	m.Card(crm)
	if v := m.View(); len(v.Visible) != 2 || v.Unread != 0 {
		t.Fatalf("%+v", v)
	}
	m.Tick(later(1))
	if v := m.View(); len(v.Visible) != 0 || v.Unread != 0 {
		t.Fatalf("%+v", v)
	}
	if _, ok := m.Get("test"); ok {
		t.Fatal("the check is forgotten")
	}
}

func TestUnreadCountsServerRest(t *testing.T) {
	m := New()
	m.Sync(api.Snapshot{Cards: []api.Card{card("a", 1, 1)}, Total: 8})
	if v := m.View(); v.Unread != 8 {
		t.Fatalf("%+v", v)
	}
}

func TestHiddenCardsAreBounded(t *testing.T) {
	m := New()
	for i := 0; i < MaxKept+50; i++ {
		m.Card(card(fmt.Sprint("k", i), 1, i))
		if i%Max == 0 {
			m.Tick(later(i + 1))
		}
	}
	if v := m.View(); v.Unread != MaxKept {
		t.Fatalf("kept %d", v.Unread)
	}
}
