package api

import (
	"context"
	"database/sql"
	"net/http"
)

const msgGovernanceCredential = "The governance model credential requires OWNER or ADMIN"

// ctxCredentialWriteRole carries the caller's real workspace role through the
// internal credential adapter, whose request context says ADMIN.
type ctxCredentialWriteRole struct{}

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// credentialGovernanceBound reports whether a credential is bound as a
// workspace governance model. Binding takes OWNER or ADMIN, and the bound value
// decides where the workspace judge connects, so a narrower role must not
// change it either (#5024). Called inside the write's transaction, a binding
// committed after this read makes that write fail instead of slip through.
func credentialGovernanceBound(ctx context.Context, q rowQueryer, credID string) (bool, error) {
	var bound bool
	err := q.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM keeper_governance_settings WHERE gov_model_credential_id = ?)`,
		credID).Scan(&bound)
	return bound, err
}

// allowGovernanceCredentialWrite answers the request and returns false when the
// credential is bound or the lookup failed. Callers skip it for OWNER and ADMIN.
func (h *CredentialHandler) allowGovernanceCredentialWrite(w http.ResponseWriter, r *http.Request, q rowQueryer, credID string) bool {
	bound, err := credentialGovernanceBound(r.Context(), q, credID)
	if err != nil {
		replyInternalError(w, h.logger, "check governance credential binding", err)
		return false
	}
	if bound {
		replyError(w, http.StatusForbidden, msgGovernanceCredential)
		return false
	}
	return true
}

// allowEscalationCredentialWrite covers rows bound before binding required an
// ACTIVE credential: such a row could still be waiting on its escalation.
func (h *QueryHandler) allowEscalationCredentialWrite(w http.ResponseWriter, r *http.Request, credentialID sql.NullString) bool {
	if !credentialID.Valid || credentialID.String == "" || canRole(RoleFromContext(r.Context()), "manage") {
		return true
	}
	bound, err := credentialGovernanceBound(r.Context(), h.db, credentialID.String)
	if err != nil {
		replyInternalError(w, h.logger, "check governance credential binding", err)
		return false
	}
	if bound {
		replyError(w, http.StatusForbidden, msgGovernanceCredential)
		return false
	}
	return true
}
