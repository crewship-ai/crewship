package api

import (
	"net/http"
)

// allowGovernanceCredentialWrite refuses a write to a credential bound as the
// workspace governance model. Callers skip it for OWNER/ADMIN: only they can
// bind the credential, and the bound value decides where the workspace judge
// connects, so a narrower role must not change it either (#5024). It writes
// the response and returns false when the caller must stop; a failed lookup
// refuses too.
func (h *CredentialHandler) allowGovernanceCredentialWrite(w http.ResponseWriter, r *http.Request, workspaceID, credID string) bool {
	var bound bool
	err := h.db.QueryRowContext(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM keeper_governance_settings
		                WHERE workspace_id = ? AND gov_model_credential_id = ?)`,
		workspaceID, credID).Scan(&bound)
	if err != nil {
		replyInternalError(w, h.logger, "check governance credential binding", err)
		return false
	}
	if bound {
		replyError(w, http.StatusForbidden, "The governance model credential requires OWNER or ADMIN")
		return false
	}
	return true
}
