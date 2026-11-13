package queue

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
)

// The category of a card (contract 1.13) is read and compared.
func TestCategoryCard(t *testing.T) {
	var c api.Card
	raw := `{"key":"chat-1","kind":"dm","category":"colleague_direct","title":"Мария","body":"Привет!","count":1,"sound":"other","at":"2026-11-13T10:00:00Z"}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Category != "colleague_direct" {
		t.Fatalf("category %q", c.Category)
	}
	d := c
	d.Category = "group_message"
	if api.Same(c, d) {
		t.Fatal("category is not compared")
	}
}

// The strip knows where the cards it counts come from: newest first, each once, none of the screen.
func TestHiddenCategories(t *testing.T) {
	m := New()
	at := time.Now()
	cats := []string{"queue", "task", "queue", "", "mention", "call_missed", "client_message"}
	for i, cat := range cats {
		m.Card(api.Card{Key: fmt.Sprint("chat-", i), Kind: "dm", Category: cat, Title: "x", At: at.Add(time.Duration(i) * time.Second)})
	}
	v := m.View()
	// the three newest (call_missed, client_message, mention) are on the screen
	if want := []string{"", "queue", "task"}; !slices.Equal(v.HiddenCategories, want) {
		t.Fatalf("hidden categories %q, want %q", v.HiddenCategories, want)
	}
	m.Browse()
	if v := m.View(); v.HiddenCategories != nil {
		t.Fatalf("browsing: %q", v.HiddenCategories)
	}
}
