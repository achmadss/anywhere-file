//go:build !(darwin && cgo)

package main

import (
	"context"
	"errors"
	"log/slog"
)

func menubarCommand(context.Context, config, *slog.Logger) error {
	return errors.New("the menu bar item is macOS only, and needs an agent built with cgo")
}
