package main

import (
	"fmt"
	"log/slog"
	"os"
	"time"
)

// config is read from the environment once at startup. Everything has a working local
// default except the database URL, which has no safe default to guess.
//
// The mailer's own settings are read in mail.go, and the session cookie's in auth.go,
// because the handlers that use them take a database and a logger rather than a config.
// docs/running-the-control-plane.md lists every variable in one table.
type config struct {
	addr            string        // RFM_ADDR
	databaseURL     string        // RFM_DATABASE_URL
	tlsCert         string        // RFM_TLS_CERT
	tlsKey          string        // RFM_TLS_KEY
	logLevel        slog.Level    // RFM_LOG_LEVEL
	shutdownTimeout time.Duration // RFM_SHUTDOWN_TIMEOUT
}

func loadConfig() (config, error) {
	c := config{
		addr:            env("RFM_ADDR", ":8443"),
		databaseURL:     os.Getenv("RFM_DATABASE_URL"),
		tlsCert:         os.Getenv("RFM_TLS_CERT"),
		tlsKey:          os.Getenv("RFM_TLS_KEY"),
		shutdownTimeout: 20 * time.Second,
	}
	if c.databaseURL == "" {
		return c, fmt.Errorf("RFM_DATABASE_URL is not set")
	}
	if (c.tlsCert == "") != (c.tlsKey == "") {
		return c, fmt.Errorf("RFM_TLS_CERT and RFM_TLS_KEY must be set together")
	}
	if err := c.logLevel.UnmarshalText([]byte(env("RFM_LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("RFM_LOG_LEVEL: %w", err)
	}
	if d := os.Getenv("RFM_SHUTDOWN_TIMEOUT"); d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			return c, fmt.Errorf("RFM_SHUTDOWN_TIMEOUT: %w", err)
		}
		c.shutdownTimeout = parsed
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
