package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/auth"
)

type Router struct {
	mux    *http.ServeMux
	db     *sql.DB
	logger *slog.Logger
	authMw *AuthMiddleware
}

func NewRouter(db *sql.DB, jwtSecret string, logger *slog.Logger) (*Router, error) {
	validator, err := auth.NewJWTValidator(jwtSecret, "")
	if err != nil {
		return nil, err
	}

	authMw := NewAuthMiddleware(validator, db, logger)

	r := &Router{
		mux:    http.NewServeMux(),
		db:     db,
		logger: logger,
		authMw: authMw,
	}

	r.registerRoutes()
	return r, nil
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) registerRoutes() {
	ws := NewWorkspaceHandler(r.db, r.logger)
	crews := NewCrewHandler(r.db, r.logger)
	agents := NewAgentHandler(r.db, r.logger)
	creds := NewCredentialHandler(r.db, r.logger)
	skills := NewSkillHandler(r.db, r.logger)
	runs := NewRunHandler(r.db, r.logger)
	audit := NewAuditHandler(r.db, r.logger)

	authed := r.authMw.RequireAuth
	wsCtx := r.authMw.RequireWorkspace

	// Health (no auth)
	r.mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Workspaces (auth only, no workspace context needed)
	r.mux.Handle("GET /api/v1/workspaces", authed(http.HandlerFunc(ws.List)))
	r.mux.Handle("POST /api/v1/workspaces", authed(http.HandlerFunc(ws.Create)))

	// Crews (require workspace context)
	r.mux.Handle("GET /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.List))))
	r.mux.Handle("POST /api/v1/crews", authed(wsCtx(http.HandlerFunc(crews.Create))))

	// Agents (require workspace context)
	r.mux.Handle("GET /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.List))))
	r.mux.Handle("POST /api/v1/agents", authed(wsCtx(http.HandlerFunc(agents.Create))))
	r.mux.Handle("GET /api/v1/agents/{agentId}", authed(wsCtx(http.HandlerFunc(agents.Get))))

	// Credentials (require workspace context + manage role for create)
	r.mux.Handle("GET /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.List))))
	r.mux.Handle("POST /api/v1/credentials", authed(wsCtx(http.HandlerFunc(creds.Create))))

	// Skills (require auth)
	r.mux.Handle("GET /api/v1/skills", authed(wsCtx(http.HandlerFunc(skills.List))))

	// Runs (require workspace context)
	r.mux.Handle("GET /api/v1/runs", authed(wsCtx(http.HandlerFunc(runs.List))))

	// Audit logs (require workspace context + manage role)
	r.mux.Handle("GET /api/v1/audit", authed(wsCtx(http.HandlerFunc(audit.List))))
}
