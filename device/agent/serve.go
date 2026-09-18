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
	// Made here rather than on the first handshake, so a PC that cannot build one says so
	// at startup instead of refusing every client later.
	cert := &lanCert{key: key}
	if _, err := cert.get(nil); err != nil {
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
		defer ad.close()
		// Announcing is retried rather than required (#124). macOS asks the person at the
		// machine whether this program may use the local network, and until they say yes
		// multicast fails. A PC that cannot announce itself is still reachable at its
		// address, so the agent keeps serving and says in the log what is missing.
		go advertiseUntil(ctx, ad, st, key, log)
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

// advertiseUntil keeps trying to announce this PC. The usual reason for a failure is a
// permission that has not been granted yet, and those are granted while the agent runs.
func advertiseUntil(ctx context.Context, ad *advertiser, st *state, key deviceKey, log *slog.Logger) {
	for wait := time.Second; ; wait = min(wait*2, time.Minute) {
		err := ad.advertise(st, key)
		if err == nil {
			return
		}
		log.Error("this PC is not announcing itself on the LAN, so clients have to be given its address",
			"err", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(jittered(wait)):
		}
	}
}
