package api

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMooringAgentPrincipalContextMatchesHTTPOriginalBinding(t *testing.T) {
	for _, mode := range []string{"valid", "source-ABA", "member", "workspace-token", "crew-token"} {
		t.Run(mode, func(t *testing.T) {
			f := originalUserPrincipalFixture(t)
			ctx := t.Context()
			receipt := *f.receipt
			switch mode {
			case "source-ABA":
				mustExec(t, f.rig.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='original-session'; UPDATE user_sessions SET revoked_at=NULL WHERE id='original-session'`)
			case "member":
				receipt.MemberID = "different-original-member"
			case "workspace-token":
				ctx = context.WithValue(ctx, ctxInternalTokenWS, f.a.WorkspaceID)
			case "crew-token":
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
			}
			r := httptest.NewRequest("POST", "/unused-principal-wrapper", nil).WithContext(ctx)
			httpErr := captureOriginalUserArtifactPrincipalQuery(r, f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, &receipt)
			var expiryBefore string
			if httpErr == nil {
				if err := f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&expiryBefore); err != nil {
					t.Fatal(err)
				}
			}
			contextErr := captureOriginalUserArtifactPrincipalContextQuery(ctx, f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, &receipt)
			wantAllowed := mode == "valid"
			if (httpErr == nil) != wantAllowed || (contextErr == nil) != wantAllowed {
				t.Fatalf("HTTP/context disagreement or unexpected admission: http=%v context=%v", httpErr, contextErr)
			}
			var count int
			if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&count); err != nil || count != map[bool]int{true: 1, false: 0}[wantAllowed] {
				t.Fatal("principal count", count, err)
			}
			if wantAllowed {
				var revision, kind, id, sourceExpiry, binding, expiry string
				if err := f.rig.db.QueryRow(`SELECT user_authority_revision,source_credential_kind,source_credential_id,source_credential_expires_at,source_credential_binding,expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&revision, &kind, &id, &sourceExpiry, &binding, &expiry); err != nil {
					t.Fatal(err)
				}
				if revision != receipt.UserAuthorityRevision || kind != receipt.SourceCredentialKind || id != receipt.SourceCredentialID || sourceExpiry != receipt.SourceCredentialExpiresAt || binding != receipt.SourceCredentialBinding || expiry != expiryBefore {
					t.Fatal("context retry changed original source or extended expiry")
				}
				limit, err := time.Parse(time.RFC3339Nano, expiry)
				if err != nil || time.Until(limit) < 59*time.Minute || time.Until(limit) > time.Hour {
					t.Fatal("generic principal lifetime changed", err)
				}
			}
		})
	}
}

func TestMooringAgentPrincipalContextPreservesLegacyBoundary(t *testing.T) {
	f := newArtifactPrincipalFixture(t)
	f.receipt.UserAuthorityRevision = ""
	r := httptest.NewRequest("POST", "/unused-legacy-wrapper", nil)
	if err := captureArtifactChatPrincipalQuery(r, f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	if err := captureArtifactChatPrincipalContextQuery(r.Context(), f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err != nil {
		t.Fatal("legacy context retry", err)
	}
	if err := captureOriginalUserArtifactPrincipalQuery(r, f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err == nil {
		t.Fatal("strict HTTP wrapper upgraded legacy receipt")
	}
	if err := captureOriginalUserArtifactPrincipalContextQuery(r.Context(), f.rig.db, f.a.RunID, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err == nil {
		t.Fatal("strict context helper upgraded legacy receipt")
	}
	var revision, kind string
	if err := f.rig.db.QueryRow(`SELECT user_authority_revision,source_credential_kind FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&revision, &kind); err != nil || revision != "" || kind != "" {
		t.Fatal("legacy provenance changed", err)
	}
}
