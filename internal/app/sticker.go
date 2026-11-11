package app

import (
	"context"
	"image"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/api"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/stickers"
)

// The stickers of the cards (contract 1.11): the picture comes from the folder shared by the computer, else from the
// CRM once (package stickers).

// Sticker is the picture of the sticker of a card; nil — none, or not here yet: Changed is called when it comes, and
// the card shows Body meanwhile.
func (a *App) Sticker(c api.Card) image.Image {
	if c.Sticker == nil || c.Sticker.Hash == "" {
		return nil
	}
	return a.stickerStore().Get(c.Sticker.Hash, c.Sticker.URL)
}

func (a *App) stickerStore() *stickers.Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stickers == nil {
		a.stickers = &stickers.Store{Dir: a.StickerDir, Log: a.log(), Changed: a.changed, Fetch: a.fetchSticker}
	}
	return a.stickers
}

func (a *App) fetchSticker(ctx context.Context, path string) ([]byte, error) {
	tok, err := a.token(ctx, false)
	if err != nil {
		return nil, err
	}
	return a.api.Sticker(ctx, tok, path, stickers.MaxFile)
}

// pruneStickers removes the old pictures this person may remove.
func (a *App) pruneStickers() {
	if n := stickers.Prune(a.StickerDir, time.Now()); n > 0 {
		a.log().Info("stickers: old removed", "files", n)
	}
}
