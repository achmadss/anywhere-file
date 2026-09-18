package main

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// serve runs the gateway. The agent is a service with no window, so everything it has to
// say goes to the log.
func serve(ctx context.Context, cfg config, log *slog.Logger) error {
	ag, err := openAgent(ctx, cfg, log)
	if err != nil {
		return err
	}
	key, st := ag.key, ag.snapshot()

	// The applications the registry gives a command for are started here and stopped
	// before the agent exits, after the gateway has stopped answering for them.
	appCtx, stopApps := context.WithCancel(ctx)
	waitApps := superviseApps(appCtx, st, log)
	defer func() {
		stopApps()
		waitApps()
	}()

	// The listener comes first, because the port it lands on is what the LAN is told.
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return err
	}
	// The port, taken before the listener is wrapped, is what the LAN is told below.
	port := ln.Addr().(*net.TCPAddr).Port
	cert, err := deviceCertificate(key)
	if err != nil {
		_ = ln.Close()
		return err
	}
	ln = tls.NewListener(ln, lanTLS(cert))
	handler := newGateway(ag)
	srv := &http.Server{
		Handler: handler,
		// No write timeout: a download of a large file is the point of this service, and a
		// deadline on the whole response would cut one off partway through.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	log.Info("gateway listening",
		"addr", "https://"+ln.Addr().String(), "device", key.deviceID(), "name", st.Name,
		"apps", st.appNames(), "enrolled", st.enrolled())

	if cfg.mdns {
		ad := newAdvertiser(port, log)
		// A PC nobody can find is a PC with no local mode, so this is a startup failure
		// and not a warning. Set RFM_AGENT_MDNS=off where there is no multicast to have.
		if err := ad.advertise(st, key); err != nil {
			_ = ln.Close()
			return err
		}
		defer ad.close()
	}

	// The tunnel serves the same handler as the LAN. It runs whether or not this PC is
	// enrolled yet, because enrolment can happen while the agent is running, and an
	// unenrolled agent retrying costs nothing.
	if cfg.tunnel {
		go runTunnel(ctx, ag, handler)
	}

	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
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
