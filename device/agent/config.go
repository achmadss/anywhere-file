package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// config is read from the environment once at startup. Everything has a working default,
// so an agent installed by the client's enrolment flow needs no file edited by hand.
type config struct {
	dir        string        // RFM_AGENT_DIR
	addr       string        // RFM_AGENT_ADDR, the LAN gateway
	keystore   string        // RFM_AGENT_KEYSTORE: auto, keyring or file
	mdns       bool          // RFM_AGENT_MDNS
	tunnel     bool          // RFM_AGENT_TUNNEL
	logLevel   slog.Level    // RFM_AGENT_LOG_LEVEL
	logFile    string        // RFM_AGENT_LOG_FILE, empty for stderr
	storeRetry time.Duration // how long to wait between retries on a locked key store
}

const (
	storeRetry      = 5 * time.Second
	shutdownTimeout = 20 * time.Second
)

func loadConfig() (config, error) {
	c := config{
		dir:        os.Getenv("RFM_AGENT_DIR"),
		addr:       env("RFM_AGENT_ADDR", ":7433"),
		keystore:   env("RFM_AGENT_KEYSTORE", "auto"),
		mdns:       env("RFM_AGENT_MDNS", "on") != "off",
		tunnel:     env("RFM_AGENT_TUNNEL", "on") != "off",
		logFile:    os.Getenv("RFM_AGENT_LOG_FILE"),
		storeRetry: storeRetry,
	}
	if c.dir == "" {
		dir, err := defaultDir()
		if err != nil {
			return c, err
		}
		c.dir = dir
	}
	if err := c.logLevel.UnmarshalText([]byte(env("RFM_AGENT_LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("RFM_AGENT_LOG_LEVEL: %w", err)
	}
	return c, nil
}

// defaultDir is where the agent keeps its own state.
func defaultDir() (string, error) {
	// On Windows os.UserConfigDir is %AppData%, which roams with the user profile. A key
	// that follows the user to a second PC would give two machines one identity, so the
	// agent uses the local-only directory instead.
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LocalAppData"); local != "" {
			return filepath.Join(local, "anywhere-file"), nil
		}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config directory: %w", err)
	}
	return filepath.Join(dir, "anywhere-file"), nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
