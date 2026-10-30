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
	snd := m.Sync(api.Snapshot{Cards: []api.Card{card("a", 1, 1)}, Total: 1})
	if keys(m.View()) != "" || snd != "" {
		t.Fatalf("%q %q", keys(m.View()), snd)
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
	snd := m.Sync(api.Snapshot{Cards: cards, Total: 27})
	v := m.View()
	if snd == "" || keys(v) != "n20 n19 n18 " || v.Waiting != 17 || v.More != 7 {
		t.Fatalf("%q %q waiting %d more %d", snd, keys(v), v.Waiting, v.More)
	}
	if _, ok := m.Get("old"); ok {
		t.Error("a card the server no longer has is dropped")
	}
	if snd := m.Sync(api.Snapshot{Cards: cards, Total: 27}); snd != "" {
		t.Error("nothing new: no sound")
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
