package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/journal"
)

// JournalIntegrityHandler exposes tamper-evidence checks over the audit
// journal's per-workspace hash-chain (issue #1369). The chain lets an
// operator prove, after the fact, that no journal row was altered, reordered,
// or deleted from the middle of the record.
type JournalIntegrityHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewJournalIntegrityHandler constructs the handler.
func NewJournalIntegrityHandler(db *sql.DB, logger *slog.Logger) *JournalIntegrityHandler {
	return &JournalIntegrityHandler{db: db, logger: logger}
}

// Verify walks the current workspace's journal hash-chain and reports whether
// it is intact, and if not, the first broken link.
//
// GET /api/v1/admin/journal/verify
//
// The workspace is taken from request context (the same X-Workspace-ID the
// rest of /api/v1/admin/* uses); operators verify the workspace they are
// scoped to. ADMIN+ is enforced at the route (authedAdmin) and re-checked
// here for defense in depth, matching the sibling Keeper log handler.
func (h *JournalIntegrityHandler) Verify(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	res, err := journal.VerifyChain(r.Context(), h.db, workspaceID)
	if err != nil {
		h.logger.Error("journal chain verify failed", "workspace", workspaceID, "err", err)
		replyError(w, http.StatusInternalServerError, "verification failed")
		return
	}
	writeJSON(w, http.StatusOK, res)
}
