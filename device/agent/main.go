// Command agent runs on the customer's PC. It holds the device key, and will announce
// itself on the LAN and expose registered local applications through a gateway.
//
// Subcommands:
//
//	agent run                    serve the applications and announce this PC (the default)
//	agent key                    print the device's public key, device id and fingerprint
//	agent discover               list the agents this machine can see on the LAN
//	agent settings               open the settings page in a browser
//	agent share add <dir>        share a directory, with --name to choose what it is called
//	agent share list             list what this PC shares
//	agent share rm <name>        stop sharing one
//	agent login <server>         sign this PC in by approving it in a browser
//	agent logout                 sign this PC out, leaving it serving the LAN
//	agent enrol <server> <token> register this PC with an account, for a headless machine
//	agent install                start at logon and keep running, as a user-level service
//	agent uninstall              stop doing that, leaving the key and the registry alone
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
	"strings"
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
	command := "run"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	// `RFM_AGENT_X=y` arguments are how an installed service carries its settings. A
	// Windows scheduled task has nowhere to put an environment, and a wrapper script that
	// sets one ends up being the process the scheduler owns, which leaves the agent
	// running after the task is stopped.
	args = applyEnvArgs(args)

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	// ponytail: no rotation. The agent logs events, not requests, so the file grows slowly.
	// Rotate when a PC turns up with one worth rotating.
	if cfg.logFile != "" {
		f, err := os.OpenFile(cfg.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("RFM_AGENT_LOG_FILE: %w", err)
		}
		defer f.Close()
		logTo = f
	}
	log := slog.New(slog.NewJSONHandler(logTo, &slog.HandlerOptions{Level: cfg.logLevel}))
	switch command {
	case "run":
		return serve(ctx, cfg, log)
	case "key":
		return printKey(ctx, cfg, log, out)
	case "discover":
		return printDiscovered(ctx, log, out)
	case "settings":
		return settingsCommand(ctx, cfg, out)
	case "share":
		return shareCommand(ctx, cfg, out, args)
	case "login":
		if len(args) != 1 {
			return fmt.Errorf("login needs the server address")
		}
		return loginCommand(ctx, cfg, log, out, args[0])
	case "logout":
		return logoutCommand(ctx, cfg, log, out)
	case "enrol":
		if len(args) != 2 {
			return fmt.Errorf("enrol needs a server address and an enrolment token")
		}
		return enrolCommand(ctx, cfg, log, out, args[0], args[1])
	case "install":
		return installService(cfg, log, out)
	case "uninstall":
		return uninstallService(cfg, log, out)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// applyEnvArgs sets the agent's own variables from the command line and returns what is
// left. Anything that is not one of ours stays an argument, so a typo reaches the command
// that can complain about it.
func applyEnvArgs(args []string) []string {
	var rest []string
	for _, a := range args {
		key, value, ok := strings.Cut(a, "=")
		if ok && strings.HasPrefix(key, envPrefix) {
			_ = os.Setenv(key, value)
			continue
		}
		rest = append(rest, a)
	}
	return rest
}

// printDiscovered is the first thing to run when a PC does not appear in the client: it
// says whether this machine can see it either.
func printDiscovered(ctx context.Context, log *slog.Logger, out io.Writer) error {
	seen, err := discover(ctx, discoverTimeout, log)
	if err != nil {
		return err
	}
	for _, f := range seen {
		fmt.Fprintf(out, "%s\t%s\t%s\tv%s\t%s\n", f.Address, f.DeviceID, f.Name, f.Version, strings.Join(f.Apps, ","))
	}
	fmt.Fprintf(out, "%d found\n", len(seen))
	return nil
}

// enrolCommand is the headless form of what the client does over the LAN. A PC with no
// screen is enrolled from its own terminal instead.
func enrolCommand(ctx context.Context, cfg config, log *slog.Logger, out io.Writer, server, token string) error {
	ag, err := openAgent(ctx, cfg, log)
	if err != nil {
		return err
	}
	st, err := ag.enrol(ctx, server, token)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "enrolled with %s\n", st.Server)
	fmt.Fprintf(out, "device id:   %s\n", st.DeviceID)
	fmt.Fprintf(out, "name:        %s\n", st.Name)
	fmt.Fprintf(out, "apps:        %s\n", strings.Join(st.appNames(), ", "))
	return nil
}

// openAgent loads what every command needs: the device key and the registry.
func openAgent(ctx context.Context, cfg config, log *slog.Logger) (*agent, error) {
	store, err := openSeedStore(cfg)
	if err != nil {
		return nil, err
	}
	key, err := waitForDeviceKey(ctx, store, log, cfg.storeRetry)
	if err != nil {
		return nil, err
	}
	st, err := loadState(cfg.dir)
	if err != nil {
		return nil, err
	}
	return newAgent(cfg.dir, key, st, log), nil
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
