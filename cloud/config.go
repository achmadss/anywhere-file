package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// config is read from the environment once at startup. Everything has a working local
// default except the database URL, which has no safe default to guess.
type config struct {
	addr            string        // RFM_ADDR
	databaseURL     string        // RFM_DATABASE_URL
	tlsCert         string        // RFM_TLS_CERT
	tlsKey          string        // RFM_TLS_KEY
	logLevel        slog.Level    // RFM_LOG_LEVEL
	shutdownTimeout time.Duration // RFM_SHUTDOWN_TIMEOUT
	overcommitRatio float64       // RFM_OVERCOMMIT_RATIO
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
	// The overcommit ratio is an operator setting with a conservative default
	// (r3 section 11.3). At 1 the fleet carries no more committed rate than
	// measured capacity. The operator raises it against real usage.
	c.overcommitRatio = 1
	if v := os.Getenv("RFM_OVERCOMMIT_RATIO"); v != "" {
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return c, fmt.Errorf("RFM_OVERCOMMIT_RATIO: %w", err)
		}
		if parsed <= 0 {
			return c, fmt.Errorf("RFM_OVERCOMMIT_RATIO must be above zero, got %q", v)
		}
		c.overcommitRatio = parsed
	}
	return c, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
