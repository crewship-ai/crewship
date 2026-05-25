package api

// System-level routes: health, runtime info, license, keeper status.
// These are small, cross-cutting endpoints that don't belong to any
// domain handler; grouping them in one file keeps router_routes.go
// focused on per-domain registration.

import "net/http"

// registerSystemRoutes wires health + system-info endpoints.
//
//	GET /api/health                  (no auth)
//	GET /api/v1/system/setup-status  (no auth — first-run gate)
//	GET /api/v1/system/telemetry     (no auth — Sentry consent gate)
//	GET /api/v1/system/runtime       (auth)
//	GET /api/v1/system/version       (auth)
//	GET /api/v1/system/license       (auth)
//	GET /api/v1/system/keeper        (auth)
//	GET /api/v1/system/aux-status    (auth — PR-B F3 diagnostic)
func (r *Router) registerSystemRoutes() {
	authed := r.authMw.RequireAuth

	// Health (no auth)
	r.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// First-run gate (no auth) — tells the login page whether to
	// redirect a visitor to /bootstrap on an empty database.
	//
	setupH := NewSetupStatusHandler(r.db, r.logger, r.allowSignup)
	r.mux.HandleFunc("GET /api/v1/system/setup-status", setupH.Status)

	// Telemetry consent gate (no auth) — read by the frontend's
	// sentry.client.config before calling Sentry.init. Must be
	// reachable pre-login so the login page itself can be covered
	// by crash reporting once consent is on. Read-only; consent is
	// flipped via the CLI (`crewship telemetry on/off`), not over HTTP.
	telemetryH := NewTelemetryStatusHandler(r.db, r.logger)
	r.mux.HandleFunc("GET /api/v1/system/telemetry", telemetryH.Status)

	// System info (auth required).
	//
	// Runtime info doesn't depend on r.version so we can capture once at
	// registration time. The version handler however reads r.version,
	// which cmd_start calls SetVersion() on AFTER router construction —
	// capturing it here at register time would serve the empty/stale
	// initial string for the entire process lifetime. Wrap in a closure
	// so each request re-reads the current r.version value at call time.
	// CodeRabbit caught this on review.
	system := NewSystemHandler(r.logger, r.version)
	r.mux.Handle("GET /api/v1/system/runtime", authed(http.HandlerFunc(system.Runtime)))
	r.mux.Handle("GET /api/v1/system/version", authed(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		NewSystemHandler(r.logger, r.version).Version(w, req)
	})))

	// License info (auth required)
	licenseH := NewLicenseHandler(r.license)
	r.mux.Handle("GET /api/v1/system/license", authed(http.HandlerFunc(licenseH.Status)))

	// Keeper status (auth required)
	keeperStatus := NewKeeperStatusHandler(r.db, r.keeperConfig, r.keeperGK, r.logger)
	r.mux.Handle("GET /api/v1/system/keeper", authed(http.HandlerFunc(keeperStatus.Status)))

	// PR-B F3 aux-status (auth required). Diagnostic read of the
	// resolved provider/model/timeout per Slot. Pulls config through
	// AuxModels() so test/dev builds that didn't wire
	// WithAuxiliaryModels still see the MVP defaults rather than
	// 5 empty rows.
	auxStatus := NewAuxStatusHandler(r.AuxModels(), r.logger)
	r.mux.Handle("GET /api/v1/system/aux-status", authed(http.HandlerFunc(auxStatus.Status)))
}
