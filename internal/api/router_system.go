package api

// System-level routes: health, runtime info, license, keeper status.
// These are small, cross-cutting endpoints that don't belong to any
// domain handler; grouping them in one file keeps router_routes.go
// focused on per-domain registration.

import "net/http"

// registerSystemRoutes wires health + system-info endpoints.
//
//	GET /api/health            (no auth)
//	GET /api/v1/system/runtime (auth)
//	GET /api/v1/system/license (auth)
//	GET /api/v1/system/keeper  (auth)
func (r *Router) registerSystemRoutes() {
	authed := r.authMw.RequireAuth

	// Health (no auth)
	r.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// System info (auth required)
	system := NewSystemHandler(r.logger)
	r.mux.Handle("GET /api/v1/system/runtime", authed(http.HandlerFunc(system.Runtime)))

	// License info (auth required)
	licenseH := NewLicenseHandler(r.license)
	r.mux.Handle("GET /api/v1/system/license", authed(http.HandlerFunc(licenseH.Status)))

	// Keeper status (auth required)
	keeperStatus := NewKeeperStatusHandler(r.db, r.keeperConfig, r.keeperGK, r.logger)
	r.mux.Handle("GET /api/v1/system/keeper", authed(http.HandlerFunc(keeperStatus.Status)))
}
