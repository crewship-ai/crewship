package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

// StartPProfServer starts an HTTP server exposing the standard Go runtime
// profiling endpoints (pprof + expvar) on addr. The default Crewship
// HTTP mux deliberately 404s /debug/pprof/* because exposing CPU profile
// generation over a public surface is both an info leak and a denial-of-
// service vector (a 30-second CPU profile blocks all other GC). Running
// pprof on a dedicated address that the operator binds to a loopback
// interface keeps both properties locked down while still giving on-call
// engineers a profile when they need one.
//
// addr should be a loopback bind like "127.0.0.1:6060". Public binds
// ("0.0.0.0:6060", ":6060") are accepted but emit a WARN — the security
// boundary here is whoever can reach the bind address.
//
// Empty addr disables the endpoint entirely; that's the production
// default so a misconfigured deploy never accidentally publishes
// profile traffic.
//
// Returned shutdown drains in-flight profile downloads with a short
// timeout. Callers should defer it alongside the main server shutdown.
func StartPProfServer(addr string, logger *slog.Logger) (shutdown func(), err error) {
	if addr == "" {
		return func() {}, nil
	}
	if logger == nil {
		logger = slog.Default()
	}

	// Build a private mux instead of using http.DefaultServeMux — importing
	// _ "net/http/pprof" elsewhere registers on Default, but the rest of
	// the binary stays away from Default so we keep the same hygiene here.
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil || (host != "" && host != "127.0.0.1" && host != "::1" && host != "localhost") {
		logger.Warn("CREWSHIP_PPROF_ADDR is not a loopback bind — anyone reachable on this address can pull CPU/heap profiles and trigger DoS via /debug/pprof/profile",
			"addr", addr,
			"recommended", "127.0.0.1:6060")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	go func() {
		logger.Info("pprof endpoint listening", "addr", addr, "url", "http://"+addr+"/debug/pprof/")
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof server exited", "err", err)
		}
	}()

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
