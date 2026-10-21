package api

import (
	"net/http"
	"testing"
)

func TestGDPRErasureRetiredOnlyCredentialsNeedNoCustodian(t *testing.T) {
	r := pagesIdentitySetup(t)
	pidExec(t, r.db, `UPDATE workspace_members SET role='OWNER' WHERE user_id=? AND workspace_id=?`, r.userID, pidWSA)
	pidExec(t, r.db, `DELETE FROM workspace_members WHERE user_id=? AND workspace_id=?`, r.adminID, pidWSA)
	// A real prior retirement remains history, not an active vault to hand over.
	pidExec(t, r.db, `UPDATE credentials SET deleted_at='2026-10-18T00:00:00Z',encrypted_value='' WHERE workspace_id=? AND created_by=?`, pidWSA, r.userID)
	r.eraseAs(t, pidWSA, r.userID, http.StatusAccepted)
	var anonymous bool
	if err := r.db.QueryRow(`SELECT created_by IS NULL FROM credentials WHERE id='pid-cr-a'`).Scan(&anonymous); err != nil || !anonymous {
		t.Fatalf("retired-only erasure retained creator: %v", err)
	}
}

func TestGDPRErasureRetiredCreatorIsAnonymizedWithLiveCustodian(t *testing.T) {
	r := pagesIdentitySetup(t)
	pidExec(t, r.db, `UPDATE credentials SET deleted_at='2026-01-01' WHERE id='pid-cr-a'`)
	r.eraseAs(t, pidWSA, r.adminID, http.StatusAccepted)
	var anonymous bool
	if err := r.db.QueryRow(`SELECT created_by IS NULL AND created_by_actor_id IS NULL AND approved_by_user_id IS NULL FROM credentials WHERE id='pid-cr-a'`).Scan(&anonymous); err != nil || !anonymous {
		t.Fatalf("retired creator identity was retained/reassigned rather than anonymized: %v", err)
	}
}
