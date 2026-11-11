package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stickers"
)

// A card with a sticker: the picture is fetched with the key of the app, kept in the folder of the computer, and the
// card without a hash shows its text.
func TestStickerOfCard(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 192, 192))
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	sum := sha256.Sum256(b.Bytes())
	hash := hex.EncodeToString(sum[:])
	path := "/api/v1/desktop/stickers/" + hash + ".png"
	var asked atomic.Int32
	crm := &replyCRM{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			asked.Add(1)
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "", 403)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(b.Bytes())
			return
		}
		crm.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := online(t, ctx, srv.URL)
	eventually(t, "online", func() bool { return a.View().State == Online })
	dir := t.TempDir() // as a.StickerDir, set before Start in the app
	a.mu.Lock()
	a.stickers = &stickers.Store{Dir: dir, Fetch: a.fetchSticker, Changed: a.changed}
	a.mu.Unlock()

	c := api.Card{Key: "chat-9", Body: "Стикер", Sticker: &api.Sticker{Code: "cats/1", Hash: hash, URL: path}}
	eventually(t, "picture", func() bool { return a.Sticker(c) != nil })
	if _, err := os.Stat(filepath.Join(dir, hash+".png")); err != nil {
		t.Fatal("not kept in the folder")
	}
	a.Sticker(c)
	if asked.Load() != 1 {
		t.Fatalf("asked %d times", asked.Load())
	}
	if a.Sticker(api.Card{Body: "Стикер", Sticker: &api.Sticker{Code: "cats/2"}}) != nil || a.Sticker(api.Card{}) != nil {
		t.Fatal("a picture without a hash")
	}
}
