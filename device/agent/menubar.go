package main

import (
	"context"
	"io"
	"log/slog"
)

// openCommand is what opening the app does: it undoes a Quit, then opens the page.
func openCommand(ctx context.Context, cfg config, log *slog.Logger, out io.Writer) error {
	p, err := currentPlan(cfg)
	if err != nil {
		return err
	}
	if err := runPlan(p.reopen, log, out); err != nil {
		return err
	}
	return settingsCommand(ctx, cfg, out)
}
