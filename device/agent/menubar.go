package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
)

// openCommand is what opening the app does: it undoes a Quit, then opens the page.
func openCommand(ctx context.Context, cfg config, log *slog.Logger, out io.Writer) error {
	// One agent per PC (#222). With this user's own agent stopped, one that answers is
	// another user's, and starting ours would only wait for its ports.
	if lock, err := lockInstance(cfg.dir, "run"); err == nil {
		_ = lock.Close()
		if agentAnswers(cfg.addr) {
			tellPerson(otherUser)
			return errors.New(strings.TrimSuffix(otherUser, "."))
		}
	}
	p, err := currentPlan(cfg)
	if err != nil {
		return err
	}
	// The installer sets up the service for whoever ran it. Anybody else on this PC gets
	// theirs the first time they open the app.
	if _, err := os.Stat(p.files[0].path); errors.Is(err, fs.ErrNotExist) {
		if err := installService(cfg, log, out); err != nil {
			return err
		}
	} else if err := runPlan(p.reopen, log, out); err != nil {
		return err
	}
	return settingsCommand(ctx, cfg, out)
}
