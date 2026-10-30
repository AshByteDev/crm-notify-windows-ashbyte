//go:build windows

package main

import (
	"context"
	"os"
	"slices"
	"time"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/winui"
)

func main() {
	// one copy per user; after an update the old copy is still closing — wait for it instead of «уже запущен»
	running := winui.AlreadyRunning()
	if slices.Contains(os.Args[1:], "--updated") {
		for i := 0; i < 50 && running; i++ {
			time.Sleep(200 * time.Millisecond)
			running = winui.AlreadyRunning()
		}
	}
	if running {
		winui.Message("AshCRM Notify уже запущен — значок в области уведомлений (у часов).")
		return
	}
	a, err := newApp()
	if err != nil {
		winui.Message("Не удалось запустить AshCRM Notify: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &winui.UI{A: a, Ctx: ctx}
	a.Changed, a.Quit = u.Notify, u.Quit
	a.Start(ctx)
	if err := u.Run(); err != nil {
		winui.Message("AshCRM Notify: " + err.Error())
	}
}
