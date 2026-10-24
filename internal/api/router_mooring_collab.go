package api

import "net/http"

// The original ingress stamp precedes authentication and body decoding. Page
// policy is enforced by the controller with the exact verified source; this
// self-scoped declaration does not make workspace membership a content grant.
func (r *Router) registerMooringRoutes() {
	if !r.mooringHTTPConfig.Enabled {
		return
	}
	manager, err := newCollaborationHTTPManager(r.pages, r.mooringHTTPConfig)
	if err != nil {
		r.mooringStartupErr = err
		return
	}
	r.mooringHTTP = manager
	r.recordMut("POST", "/api/v1/pages/{slug}/mooring/session", roleSelf, scopeSelf)
	register := func(method, path string, h http.HandlerFunc) {
		r.mux.Handle(method+" "+path, manager.fence.Wrap(r.authMw.RequireAuth(r.authMw.RequireWorkspace(r.providerAccountPolicy(h)))))
	}
	register("POST", "/api/v1/pages/{slug}/mooring/session", manager.session)
	register("GET", "/api/v1/pages/{slug}/mooring/ws", manager.websocket)
}
