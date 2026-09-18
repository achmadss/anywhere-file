package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
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
	apps := newSupervisor(appCtx, log)
	apps.set(st.Apps)
	defer func() {
		stopApps()
		apps.wait()
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
	// What happens when somebody changes what this PC shares. The applications come first,
	// so an added one is more likely to be answering by the time its route appears.
	ag.onApps = func(list []app) {
		apps.set(list)
		handler.rebuild()
		log.Info("the registry changed", "apps", appNames(list))
	}
	if err := serveSettings(ctx, cfg, ag); err != nil {
		_ = ln.Close()
		return err
	}
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
	// A share added while this PC was offline is on disk and not on the server, and the
	// server routes remote requests by the list it holds.
	go ag.pushApps(ctx)

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

// serveSettings starts the loopback endpoint that `agent share` and the settings page both
// use (#136). It is a second listener because the gateway is on the LAN, where access is
// open by design, and choosing what the whole PC shares must not be.
func serveSettings(ctx context.Context, cfg config, ag *agent) error {
	if cfg.settings == "" {
		return nil
	}
	token, err := settingsToken(cfg.dir)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.settings)
	if err != nil {
		// Refused rather than carried on without. `agent share` writes the registry
		// itself when nothing answers here, and doing that while this agent is serving
		// would leave the two disagreeing until the next restart.
		return fmt.Errorf("%w. Another agent may be running. Set RFM_AGENT_SETTINGS_ADDR to another loopback port, or to off", err)
	}
	srv := &http.Server{
		Handler:           newSettings(ag, token),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			ag.log.Error("the settings endpoint stopped", "err", err)
		}
	}()
	ag.log.Info("settings listening", "addr", "http://"+ln.Addr().String())
	return nil
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
