//go:build windows

package main

import (
	"context"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/winui"
)

func main() {
	if winui.AlreadyRunning() {
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
	a.Changed = u.Notify
	a.Start(ctx)
	if err := u.Run(); err != nil {
		winui.Message("AshCRM Notify: " + err.Error())
	}
}
