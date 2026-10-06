//go:build (darwin && cgo) || windows

package main

// What the tray menu does, on the systems that have one: the macOS menu bar (#159) and the
// Windows notification area (#160). Both do what `agent settings` does, so nothing in them
// is needed to configure a PC.

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// openSettingsFromMenu is the menu's "Open settings". Off the menu's own thread, so the
// menu closes while the agent is asked for its token.
func openSettingsFromMenu(cfg config, log *slog.Logger) {
	go func() {
		if err := settingsCommand(context.Background(), cfg, io.Discard); err != nil {
			log.Error("the tray menu could not open the settings page", "err", err)
		}
	}()
}

// quitFromMenu stops sharing, and the icon with it, until the app is opened again.
func quitFromMenu(cfg config, log *slog.Logger) {
	p, err := currentPlan(cfg)
	if err == nil {
		err = runPlan(p.quit, log, io.Discard)
	}
	if err != nil {
		log.Error("quit from the tray menu", "err", err)
	}
	os.Exit(0)
}
