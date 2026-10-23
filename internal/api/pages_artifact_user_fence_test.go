package api

import (
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
)

// The receipt was admitted before the user-wide revocation. A late run bearer
// must not attach today's user authority to that old human message.
func TestArtifactPrincipalOriginalUserRevocationPreventsLateCapture(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at='2026-10-23T21:00:00Z' WHERE id=?`, f.a.HumanUserID)
	err := f.capture(httptest.NewRequest("POST", "/", nil), f.a.MessageID, f.receipt)
	var principals int
	if queryErr := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&principals); queryErr != nil {
		t.Fatal(queryErr)
	}
	if err == nil || principals != 0 {
		t.Fatalf("old trusted receipt acquired post-revocation run authority: err=%v retained=%d", err, principals)
	}
}

func TestArtifactPrincipalOriginalUserRevisionCannotRefreshExistingRun(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	f.admit(t)
	var originalRevision, originalExpiry string
	if err := f.rig.db.QueryRow(`SELECT user_authority_revision,expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&originalRevision, &originalExpiry); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/", nil)
	if err := f.capture(request, f.a.MessageID, f.receipt); err != nil {
		t.Fatal("exact original retry", err)
	}
	mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at='2026-10-23T21:00:00Z' WHERE id=?`, f.a.HumanUserID)
	fresh, err := chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil)
	if err != nil || fresh == nil || fresh.UserAuthorityRevision == originalRevision {
		t.Fatal("fresh source did not change after revocation", fresh, err)
	}
	if err := f.capture(request, f.a.MessageID, fresh); err == nil {
		t.Fatal("same run recaptured today's global user authority")
	}
	var retainedRevision, retainedExpiry string
	if err := f.rig.db.QueryRow(`SELECT user_authority_revision,expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&retainedRevision, &retainedExpiry); err != nil || retainedRevision != originalRevision || retainedExpiry != originalExpiry {
		t.Fatal("immutable principal or expiry changed", retainedRevision, retainedExpiry, err)
	}
	if err := f.pages.checkOriginalUserArtifactRunPrincipal(t.Context(), &f.a); err == nil {
		t.Fatal("stored old principal revived after user revocation")
	}
}

func TestArtifactPrincipalLegacyFenceRemainsLegacyAndCannotAdmitMooring(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	f.receipt.UserAuthorityRevision = ""
	r := httptest.NewRequest("POST", "/", nil)
	if err := captureOriginalUserArtifactPrincipalQuery(r, f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err == nil {
		t.Fatal("new Mooring admission accepted a missing original user fence")
	}
	f.admit(t) // Existing Docs/GridCraft admission remains legacy-compatible.
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err != nil {
		t.Fatal("legacy product authority was broken", err)
	}
	if err := f.pages.checkOriginalUserArtifactRunPrincipal(t.Context(), &f.a); err == nil {
		t.Fatal("legacy retained principal gained a new Mooring fence")
	}
	var revision string
	if err := f.rig.db.QueryRow(`SELECT user_authority_revision FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&revision); err != nil || revision != "" {
		t.Fatal("legacy empty fence was upgraded", revision, err)
	}
}
