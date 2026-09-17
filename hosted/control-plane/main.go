// Command control-plane is the hosted server of docs/new-arch.md: accounts, the device
// registry, who may reach which device, and the tunnel endpoint the agents connect to.
//
// Subcommands:
//
//	cloud serve            run the HTTP service (the default)
//	cloud migrate up       apply pending migrations
//	cloud migrate down [n] reverse the last n migrations, or all of them
//
// Configuration is environment only, see config.go.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.logLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := pgxpool.New(ctx, cfg.databaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer db.Close()

	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}

	switch command {
	case "serve":
		return serve(ctx, cfg, db, log)
	case "migrate":
		if len(args) == 0 {
			return fmt.Errorf("migrate needs a direction: up or down")
		}
		switch args[0] {
		case "up":
			return migrateUp(ctx, db, log)
		case "down":
			steps := 0
			if len(args) > 1 {
				if steps, err = strconv.Atoi(args[1]); err != nil {
					return fmt.Errorf("migrate down: %w", err)
				}
			}
			return migrateDown(ctx, db, steps, log)
		default:
			return fmt.Errorf("migrate: unknown direction %q", args[0])
		}
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
