// AshCRM Notify: notifications of AshCRM in the tray of Windows (docs/APP_SPEC.md).
package main

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/app"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/managed"
	"github.com/AshByteDev/crm-notify-windows-ashbyte/internal/platform"
)

// Set at build time (Makefile): -X main.version=… -X main.server=… (only prefills the address of the portal)
var (
	version = "dev"
	server  = ""
	repo    = "AshByteDev/crm-notify-windows-ashbyte" // releases with updates
)

// newApp builds the client from the flags and the build settings.
func newApp() (*app.App, error) {
	srv := flag.String("server", "", "адрес портала AshCRM (вместо сохранённого)")
	_ = flag.Bool("autostart", false, "запущено вместе с Windows")
	_ = flag.Bool("updated", false, "запущено после обновления")
	_ = flag.Bool("installed", false, "запущено после установки из папки администратора")
	_ = flag.Bool("uninstall", false, "удалить приложение из профиля пользователя")
	flag.Parse()
	platform.CleanupOld()
	dir, err := platform.Dir()
	if err != nil {
		return nil, err
	}
	if *srv != "" { // an explicit portal (development, a support case) replaces the saved one
		cfg := platform.LoadConfig(dir)
		if s, err := app.NormalizeServer(*srv); err == nil && s != cfg.Server {
			cfg.Server = s
			_ = platform.SaveConfig(dir, cfg)
			platform.DeleteSecret(dir)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	log := logger(dir)
	var settings *managed.Config // config.json next to the running copy
	if c, ok, err := managed.Load(filepath.Dir(exe)); err != nil {
		log.Warn("managed settings", "err", err)
	} else if ok && (*srv == "" || c.Managed()) { // an explicit portal outranks the file of an ordinary copy
		settings = &c
	}
	return &app.App{DefaultServer: server, Version: version, Repo: repo, Dir: dir, Device: app.DeviceName(), Exe: exe,
		Managed: settings, Log: log,
		P: app.Platform{OpenURL: platform.OpenURL, PlaySound: platform.PlaySound, Autostart: platform.Autostart,
			SetAutostart: platform.SetAutostart, Replace: platform.Replace, Uninstall: platform.Uninstall},
	}, nil
}

// logger writes notify.log next to the settings (no tokens are ever logged); kept under 1 MB.
func logger(dir string) *slog.Logger {
	path := filepath.Join(dir, "notify.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		_ = os.Rename(path, path+".1")
	}
	var w io.Writer = io.Discard
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		w = f
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
