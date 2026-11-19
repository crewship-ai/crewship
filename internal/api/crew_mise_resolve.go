package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
)

// CrewMiseLockHandler proposes locks without changing the crew environment.
type CrewMiseLockHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	resolver provider.CrewMiseLockResolver
	slots    chan struct{}
}

// NewCrewMiseLockHandler discovers the running provider's optional resolver.
func NewCrewMiseLockHandler(db *sql.DB, logger *slog.Logger, container provider.ContainerProvider) *CrewMiseLockHandler {
	h := &CrewMiseLockHandler{db: db, logger: logger, slots: make(chan struct{}, 2)}
	h.resolver, _ = container.(provider.CrewMiseLockResolver)
	return h
}

// Resolve returns a proposal only. It does not write configuration, publish a
// revision or replace a crew runtime. Registry access happens in the existing
// bounded disposable builder, never inside the live crew container.
// openapi: responses 200,400,401,403,404,409,429,500,502,503
func (h *CrewMiseLockHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	source := auth.OriginalCredentialFromContext(ctx)
	role := RoleFromContext(ctx)
	kind := AuthKindFromContext(ctx)
	if (role != "OWNER" && role != "ADMIN") || user == nil || source == nil || !source.Valid() || source.UserID != user.ID || user.OriginalCredential == nil || *source != *user.OriginalCredential || source.Kind != kind || InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" || projectAgentFrom(ctx) != nil || ctx.Value(artifactRunKey{}) != nil ||
		(kind != AuthKindCLIToken && kind != AuthKindSession) || (kind == AuthKindCLIToken && (user.TokenID != source.ID || user.SessionID != "")) || (kind == AuthKindSession && (user.SessionID != source.ID || user.TokenID != "")) {
		replyError(w, 403, "human OWNER or ADMIN credential is required")
		return
	}
	var selected string
	err := h.db.QueryRowContext(ctx, `SELECT COALESCE(cached_image,'') FROM crews WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, r.PathValue("crewId"), WorkspaceIDFromContext(ctx)).Scan(&selected)
	if err == sql.ErrNoRows {
		replyError(w, 404, "crew not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "query mise resolver crew", err)
		return
	}
	var count int
	if err = h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM environment_revisions WHERE workspace_id=? AND crew_id=? AND image_id=?`, WorkspaceIDFromContext(ctx), r.PathValue("crewId"), selected).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "query mise resolver revision", err)
		return
	}
	if !dockerutil.IsLocalImageID(selected) || count == 0 {
		replyError(w, 409, "crew requires a completed immutable provisioned environment")
		return
	}
	var input struct {
		Config struct {
			Tools map[string]string            `json:"tools"`
			Lock  *devcontainer.MiseLockBundle `json:"lock,omitempty"`
		} `json:"config"`
		Bump bool `json:"bump"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err = dec.Decode(&input); err != nil {
		replyError(w, 400, "invalid resolver input (only tools, lock and bump are accepted)")
		return
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		replyError(w, 400, "resolver input must contain one JSON object")
		return
	}
	cfg := &devcontainer.MiseConfig{Tools: input.Config.Tools, Lock: input.Config.Lock}
	if err = cfg.Validate(); err != nil || len(cfg.Tools) == 0 {
		replyError(w, 400, "valid mise tools and lock are required")
		return
	}
	if h.resolver == nil {
		replyError(w, 503, "remote mise resolver is unavailable")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		replyError(w, 429, "mise resolvers are busy; retry later")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := h.resolver.ResolveCrewMiseLock(ctx, selected, cfg, input.Bump)
	if err != nil {
		// Resolver inputs are tools and a lock only (no env, tasks or
		// credentials), so its error is safe to log and is the only trace an
		// operator gets of why a proposal failed (#5545).
		h.logger.Warn("crew mise lock resolution failed", "crew_id", r.PathValue("crewId"), "error", err)
		replyError(w, 502, "mise lock resolution failed; no proposal was applied")
		return
	}
	writeJSON(w, 200, result)
}
