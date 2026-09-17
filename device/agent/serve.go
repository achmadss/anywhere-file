package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// serve runs the gateway. The agent is a service with no window, so everything it has to
// say goes to the log.
func serve(ctx context.Context, cfg config, log *slog.Logger) error {
	store, err := openSeedStore(cfg)
	if err != nil {
		return err
	}
	key, err := waitForDeviceKey(ctx, store, log, cfg.storeRetry)
	if err != nil {
		return err
	}
	st, err := loadState(cfg.dir)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    cfg.addr,
		Handler: newGateway(st, key, log),
		// No write timeout: a download of a large file is the point of this service, and a
		// deadline on the whole response would cut one off partway through.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	log.Info("gateway listening",
		"addr", cfg.addr, "device", key.deviceID(), "name", st.Name, "apps", st.appNames())

	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdown)
}
