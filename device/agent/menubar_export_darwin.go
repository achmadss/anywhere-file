//go:build darwin && cgo

package main

import "C"

//export menuOpenSettings
func menuOpenSettings() { openSettingsFromMenu(menu.cfg, menu.log) }

//export menuQuit
func menuQuit() { quitFromMenu(menu.cfg, menu.log) }
