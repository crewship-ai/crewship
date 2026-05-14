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
//	GET /api/v1/system/runtime       (auth)
//	GET /api/v1/system/version       (auth)
//	GET /api/v1/system/license       (auth)
//	GET /api/v1/system/keeper        (auth)
func (r *Router) registerSystemRoutes() {
	authed := r.authMw.RequireAuth

	// Health (no auth)
	r.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// First-run gate (no auth) — tells the login page whether to
	// redirect a visitor to /bootstrap on an empty database.
	setupH := NewSetupStatusHandler(r.db, r.logger, r.allowSignup)
	r.mux.HandleFunc("GET /api/v1/system/setup-status", setupH.Status)

	// System info (auth required)
	system := NewSystemHandler(r.logger, r.version)
	r.mux.Handle("GET /api/v1/system/runtime", authed(http.HandlerFunc(system.Runtime)))
	r.mux.Handle("GET /api/v1/system/version", authed(http.HandlerFunc(system.Version)))

	// License info (auth required)
	licenseH := NewLicenseHandler(r.license)
	r.mux.Handle("GET /api/v1/system/license", authed(http.HandlerFunc(licenseH.Status)))

	// Keeper status (auth required)
	keeperStatus := NewKeeperStatusHandler(r.db, r.keeperConfig, r.keeperGK, r.logger)
	r.mux.Handle("GET /api/v1/system/keeper", authed(http.HandlerFunc(keeperStatus.Status)))
}
