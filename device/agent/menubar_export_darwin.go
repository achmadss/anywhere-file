//go:build darwin && cgo

package main

import "C"

import (
	"context"
	"io"
	"os"
)

//export menuOpenSettings
func menuOpenSettings() {
	// Off the main thread, so the menu closes while the agent is asked for its token.
	go func() {
		if err := settingsCommand(context.Background(), menu.cfg, io.Discard); err != nil {
			menu.log.Error("the menu bar could not open the settings page", "err", err)
		}
	}()
}

// menuQuit stops sharing, and the menu with it, until the app is opened again. Both jobs
// are unloaded with -w, so neither comes back at the next logon on its own.
//
//export menuQuit
func menuQuit() {
	p, err := currentPlan(menu.cfg)
	if err == nil {
		err = runPlan(p.uninstall, menu.log, io.Discard)
	}
	if err != nil {
		menu.log.Error("quit from the menu bar", "err", err)
	}
	os.Exit(0)
}
