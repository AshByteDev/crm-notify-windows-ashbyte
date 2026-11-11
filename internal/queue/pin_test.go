package queue

import (
	"testing"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// The card of an answer stays at the edge: newer cards, «Скрыть все», the CRM and the list do not take it away.
func TestPinnedStays(t *testing.T) {
	m := filled(1) // c1
	m.Pin("c1")
	for _, k := range []string{"c2", "c3", "c4", "c5"} {
		m.Card(card(k, 1, 10))
	}
	if got := keys(m.View()); got != "c1 c5 c4 " {
		t.Fatalf("visible %q", got)
	}
	m.HideAll()
	m.Clear("c1")
	m.Sync(api.Snapshot{})
	if got := keys(m.View()); got != "c1 " {
		t.Fatalf("after hide, clear, sync: %q", got)
	}
	if u := m.Unread(later()); len(u) != 0 {
		t.Fatalf("«Прочитать все» must not take it: %v", u)
	}
	m.Unpin() // the CRM dropped it meanwhile: it goes now
	if _, ok := m.Get("c1"); ok {
		t.Fatal("stale card stays after Unpin")
	}
}

// A new message in the chat of the answer makes it live again: Unpin keeps it.
func TestPinnedComesBack(t *testing.T) {
	m := filled(1)
	m.Pin("c1")
	m.Clear("c1")
	m.Card(card("c1", 2, 5))
	m.Unpin()
	if c, ok := m.Get("c1"); !ok || c.Count != 2 {
		t.Fatalf("card %v %v", c, ok)
	}
}

// In «Не беспокоить» and in the list the card of an answer is still on the screen.
func TestPinnedInDNDAndList(t *testing.T) {
	m := New()
	m.SetDND(true)
	m.Card(card("c1", 1, 1))
	m.Pin("c1")
	if got := keys(m.View()); got != "c1 " {
		t.Fatalf("dnd %q", got)
	}
	m.SetDND(false)
	for _, k := range []string{"c2", "c3", "c4", "c5"} {
		m.Card(card(k, 1, 10))
	}
	m.Browse()
	v := m.View()
	if got := keys(v); got != "c1 c5 c4 " || v.Listed != 4 {
		t.Fatalf("list %q listed %d", got, v.Listed)
	}
	m.Dismiss("c1") // «Закрыть» ends the answer too
	if _, ok := m.Get("c1"); ok {
		t.Fatal("dismissed")
	}
}
