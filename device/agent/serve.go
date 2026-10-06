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
	store, err := openSeedStore(cfg)
	if err != nil {
		return err
	}
	// The settings page comes up before the key, so a locked key store is said there and
	// not only in the log (#225). The gateway still waits for the key.
	settingsReady, err := serveSettings(ctx, cfg, store, log)
	if err != nil {
		return err
	}
	ag, err := loadAgent(ctx, cfg, store, log)
	if err != nil {
		return err
	}
	settingsReady(ag)
	key, st := ag.key, ag.snapshot()

	// The applications the registry gives a command for are started here and stopped
	// before the agent exits, after the gateway has stopped answering for them.
	appCtx, stopApps := context.WithCancel(ctx)
	apps := newSupervisor(appCtx, log, ag.moveApp)
	handler := newGateway(ag)
	// What happens when somebody changes what this PC shares. The applications come first,
	// so an added one is more likely to be answering by the time its route appears. Set
	// before anything starts, because a share that moves to a new port changes the list.
	ag.onApps = func(list []app) {
		apps.set(list)
		handler.rebuild()
		// A count, because the names are folder names (ADR 0007).
		log.Info("the registry changed", "apps", len(list))
	}
	apps.set(st.Apps)
	defer func() {
		stopApps()
		apps.wait()
	}()

	// The listener comes first, because the port it lands on is what the LAN is told.
	ln, err := listenWhenFree(ctx, cfg.addr, log)
	if err != nil {
		if ctx.Err() != nil {
			return nil // stopped while waiting for the port
		}
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
	srv := &http.Server{
		Handler: handler,
		// No write timeout: a download of a large file is the point of this service, and a
		// deadline on the whole response would cut one off partway through.
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	log.Info("gateway listening",
		"addr", "https://"+ln.Addr().String(), "device", key.deviceID(), "name", st.Name,
		"apps", len(st.Apps), "enrolled", st.enrolled())

	var ad *advertiser
	if cfg.mdns {
		ad = newAdvertiser(port, log)
		defer ad.close()
	}
	go keepAdvertised(ctx, ad, cert, ag, networkPoll)

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
// open by design, and choosing what the whole PC shares must not be. The function it
// returns hands the endpoint the agent once the device key has loaded.
func serveSettings(ctx context.Context, cfg config, store seedStore, log *slog.Logger) (func(*agent), error) {
	if cfg.settings == "" {
		return func(*agent) {}, nil
	}
	token, err := settingsToken(cfg.dir)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.settings)
	if err != nil && !addrInUse(err) {
		// Refused rather than carried on without. `agent share` writes the registry
		// itself when nothing answers here, and doing that while this agent is serving
		// would leave the two disagreeing until the next restart.
		return nil, fmt.Errorf("%w. Set RFM_AGENT_SETTINGS_ADDR to another loopback port, or to off", err)
	}
	handler, ready := settingsSwitch(store, token, log)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		// Another program has the port. Waited for here, so the gateway serves meanwhile.
		if ln == nil {
			if ln, err = listenWhenFree(ctx, cfg.settings, log); err != nil {
				return
			}
		}
		log.Info("settings listening", "addr", "http://"+ln.Addr().String())
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("the settings endpoint stopped", "err", err)
		}
	}()
	return ready, nil
}
