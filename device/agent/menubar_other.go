//go:build !(darwin && cgo) && !windows

package main

import (
	"context"
	"errors"
	"log/slog"
)

func menubarCommand(context.Context, config, *slog.Logger) error {
	return errors.New("the tray icon is for macOS and Windows, and on macOS needs an agent built with cgo")
}
