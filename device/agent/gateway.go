package main

// The local gateway (#91). One handler serves every registered application, on the LAN
// now and down the tunnel later (#95), so there is one place that decides what a request
// may reach and one place to get that decision right.
//
// The gateway forwards to names it knows and to nothing else. A request cannot name a
// host, a port or a scheme: the name is looked up in the registry read from disk, and the
// address comes from there. That is what keeps the agent from being an open proxy and the
// remote path from being an SSRF primitive.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"sync/atomic"
)

// discoveryPath is what a client reads after it finds the agent, to learn which device
// this is and what it offers.
const discoveryPath = "/.well-known/anywhere-file"

// gateway serves the applications the agent is configured for. The routes come from the
// registry, and the registry changes while the agent runs (#136), so they are rebuilt on a
// change and swapped in whole. A request in flight goes on using the routes it started on.
type gateway struct {
	ag *agent
	h  atomic.Pointer[http.Handler]
}

func newGateway(ag *agent) *gateway {
	g := &gateway{ag: ag}
	g.rebuild()
	return g
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*g.h.Load()).ServeHTTP(w, r)
}

// rebuild reads the registry again and replaces the routes. The settings endpoint calls it
// after it has written a change.
func (g *gateway) rebuild() {
	h := buildGateway(g.ag)
	g.h.Store(&h)
}

// buildGateway makes the routes for one version of the registry. What the agent learns at
// runtime, the server it belongs to, is read on each request instead.
func buildGateway(ag *agent) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+discoveryPath, func(w http.ResponseWriter, r *http.Request) {
		s := ag.snapshot()
		doc := map[string]any{
			"v":         protocolVersion,
			"device_id": ag.key.deviceID(),
			"name":      s.Name,
			"apps":      s.appNames(),
			"enrolled":  s.enrolled(),
		}
		// What ties the certificate on this connection to the device id above (#96). It
		// costs a signature per document, and a client reads one document per PC.
		if proof, err := deviceProof(ag.key); err == nil {
			doc["public_key"] = ag.key.publicHex()
			doc["tls_proof"] = proof
		} else {
			ag.log.Error("the discovery document has no proof of this device's certificate", "err", err)
		}
		writeJSON(w, http.StatusOK, doc)
	})
	for _, a := range ag.snapshot().Apps {
		h := appProxy(a, ag.log)
		// Both, so that a request for the application's root is forwarded rather than
		// redirected. The server sends `/{app}` for exactly that case.
		mux.Handle("/"+a.Name, h)
		mux.Handle("/"+a.Name+"/", h)
	}
	return gatewayGuard(mux, ag.log)
}

// appProxy forwards to one application. The inbound URL decides the path and the query
// and nothing else: the scheme and the address come from the registry.
func appProxy(a app, log *slog.Logger) http.Handler {
	prefix := "/" + a.Name
	proxy := &httputil.ReverseProxy{
		// -1 writes each chunk straight through. A download starts arriving at once and a
		// file never sits in this process.
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = a.Address
			pr.Out.Host = a.Address
			pr.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("application unreachable", "app", a.Name, "address", a.Address,
				"path", r.URL.Path, "err", err)
			http.Error(w, "application unavailable", http.StatusBadGateway)
		},
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// The name stays on the path. An application has to be told the prefix it is served
	// under anyway, or the links it writes land nowhere, and one that is told strips the
	// prefix itself: dufs does it in `extract_path`. Stripping it here as well would take
	// the request down to nothing.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix {
			r = r.Clone(r.Context())
			r.URL.Path, r.URL.RawPath = prefix+"/", ""
		}
		proxy.ServeHTTP(w, r)
	})
}

// gatewayGuard refuses what must never reach an application and answers everything else
// the mux does not know with 404. An unregistered name looks exactly like a path that does
// not exist, which is all a caller needs to learn.
func gatewayGuard(mux *http.ServeMux, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An absolute request URI names a host of the caller's choosing. Proxies accept
		// one; we are not a proxy, so it is refused before anything looks at the path.
		// The Host header is not refused, because it decides nothing here: the tunnel
		// sets it to the device id and the gateway ignores it either way.
		if r.URL.IsAbs() || r.URL.Host != "" || r.Method == http.MethodConnect {
			log.Warn("refused an absolute request", "method", r.Method, "url", r.URL.String())
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
