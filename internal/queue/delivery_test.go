package queue

import (
	"encoding/json"
	"testing"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// A card of the delivery from the CRM (contract 1.12): its kind, its sound, alert.
func TestDeliveryCard(t *testing.T) {
	var c api.Card
	raw := `{"key":"delivery-7","kind":"delivery","title":"Пинг: заявка S-12","body":"Где водитель?","url":"/delivery/12","count":1,"sound":"delivery","alert":true,"at":"2026-11-12T10:00:00Z"}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindDelivery || c.Sound != api.SoundDelivery || !c.Alert {
		t.Fatalf("card %+v", c)
	}
	m := New()
	if s := m.Card(c); s != api.SoundAlert {
		t.Fatalf("alert sound %q", s)
	}
	c.Key, c.Alert = "delivery-8", false
	if s := m.Card(c); s != api.SoundDelivery {
		t.Fatalf("delivery sound %q", s)
	}
	for sound, want := range map[string]string{"client": "client", "other": "other", "": "other", "truck": "other"} {
		if got := soundOf(api.Card{Sound: sound}); got != want {
			t.Errorf("sound %q → %q, want %q", sound, got, want)
		}
	}
	if api.Same(c, func() api.Card { d := c; d.Alert = true; return d }()) {
		t.Fatal("alert is not compared")
	}
}

// A card to be noticed stays on the screen: new ordinary cards go into the strip instead of it.
func TestAlertNotPushedOff(t *testing.T) {
	m := New()
	a := card("a", 1, 1)
	a.Alert = true
	m.Card(a)
	for _, k := range []string{"c2", "c3", "c4", "c5"} {
		m.Card(card(k, 1, 10))
	}
	if got := keys(m.View()); got != "a c5 c4 " {
		t.Fatalf("visible %q", got)
	}
	m.Dismiss("a") // closed by the person: it goes
	if got := keys(m.View()); got != "c5 c4 c3 " {
		t.Fatalf("after close %q", got)
	}
}
