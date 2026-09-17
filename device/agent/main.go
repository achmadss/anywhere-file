// Command agent runs on the customer's PC. It holds the device key, and will announce
// itself on the LAN and expose registered local applications through a gateway.
//
// Subcommands:
//
//	agent key   print the device's public key, device id and fingerprint
//
// Configuration is environment only, see config.go.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, logTo io.Writer) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(logTo, &slog.HandlerOptions{Level: cfg.logLevel}))

	command := "key"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "key":
		return printKey(ctx, cfg, log, out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// printKey is what an operator runs to read the identity off a PC, and what the client
// will compare against after discovery.
func printKey(ctx context.Context, cfg config, log *slog.Logger, out io.Writer) error {
	store, err := openSeedStore(cfg)
	if err != nil {
		return err
	}
	key, err := waitForDeviceKey(ctx, store, log, cfg.storeRetry)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "public key:  %s\n", key.publicHex())
	fmt.Fprintf(out, "device id:   %s\n", key.deviceID())
	fmt.Fprintf(out, "fingerprint: %s\n", key.fingerprint())
	return nil
}
