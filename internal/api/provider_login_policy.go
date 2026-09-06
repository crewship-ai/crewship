package api

import (
	"database/sql"
	"net/http"
)

// Provider accounts are administrative resources, including legacy API_KEY
// and AI_CLI_TOKEN rows. Capability grants for ordinary secrets do not bypass
// this boundary. Run-time credential delivery is deliberately unchanged.
func (r *Router) providerAccountPolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if canRole(RoleFromContext(req.Context()), "manage") {
			next.ServeHTTP(w, req)
			return
		}
		id := req.PathValue("credentialId")
		rotationID := req.PathValue("rotationId")
		if id == "" && rotationID == "" {
			next.ServeHTTP(w, req)
			return
		}
		query := `SELECT COALESCE(c.type, ''), COALESCE(c.provider, '') FROM credentials c WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`
		if rotationID != "" {
			id = rotationID
			query = `SELECT COALESCE(c.type, ''), COALESCE(c.provider, '') FROM credential_rotations cr JOIN credentials c ON c.id = cr.credential_id WHERE cr.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`
		}
		var typ, provider string
		err := r.db.QueryRowContext(req.Context(), query, id, WorkspaceIDFromContext(req.Context())).Scan(&typ, &provider)
		if err == sql.ErrNoRows || (err == nil && isLoginRow(typ, provider)) {
			replyError(w, http.StatusNotFound, "Credential not found")
			return
		}
		if err != nil {
			replyInternalError(w, r.logger, "provider account policy", err)
			return
		}
		next.ServeHTTP(w, req)
	})
}
