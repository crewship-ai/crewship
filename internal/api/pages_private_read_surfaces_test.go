package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/pages"
)

func privateReadSurfaceFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	id := pagesCreateWith(t, h, ws, user, strings.ReplaceAll(pagesSpecBody("private-read"), "Flotila .201", "Private read output"))
	if got := projectPut(t, h, ws, user, "OWNER", "private-read", 0, projectTestSource()); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	pagesJoinCrew(t, h, user, "crew-lookout")
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, user)
	execOrFatal(t, h.db, `UPDATE pages SET private_audience=1 WHERE id=?`, id)
	h.documentCall = func(_ context.Context, _, _, _, operation string, _ any) ([]byte, int, error) {
		switch operation {
		case "read":
			return []byte(`{"content":{"type":"doc"},"can_write":true}`), 200, nil
		case "checkpoints":
			return []byte(`{"checkpoints":[]}`), 200, nil
		case "checkpoint-read":
			return []byte(`{"id":"pin-one","rev":1,"schema":3,"content":{"type":"doc"}}`), 200, nil
		default:
			t.Fatalf("unexpected document operation %q", operation)
			return nil, 500, nil
		}
	}
	return h, ws, user, id
}

var privateReadSurfaces = []string{"versions", "export", "document", "document-history", "document-checkpoint", "project", "project-source", "project-history", "project-revision", "project-revision-source"}

func privateReadSurfaceCall(t *testing.T, h *PageHandler, ws, user, role, surface string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesRequest(t, http.MethodGet, "/private-read/"+surface, ws, user, role, "")
	r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindSession))
	r.SetPathValue("slug", "private-read")
	r.SetPathValue("checkpoint", "pin-one")
	r.SetPathValue("revision", "1")
	out := httptest.NewRecorder()
	switch surface {
	case "versions":
		h.ListVersions(out, r)
	case "export":
		h.Export(out, r)
	case "document":
		h.GetDocument(out, r)
	case "document-history":
		h.ListDocumentCheckpoints(out, r)
	case "document-checkpoint":
		h.ReadDocumentCheckpoint(out, r)
	case "project":
		h.GetProject(out, r)
	case "project-source":
		h.GetProjectSource(out, r)
	case "project-history":
		h.ProjectHistory(out, r)
	case "project-revision":
		h.GetProjectRevision(out, r)
	case "project-revision-source":
		h.GetProjectRevisionSource(out, r)
	default:
		t.Fatal("unknown surface")
	}
	return out
}

func TestPrivateReadSurfacesPreserveRestrictedCustody(t *testing.T) {
	for _, surface := range privateReadSurfaces {
		t.Run(surface, func(t *testing.T) {
			h, ws, user, _ := privateReadSurfaceFixture(t)
			if got := privateReadSurfaceCall(t, h, ws, user, "MEMBER", surface); got.Code != 200 {
				t.Fatalf("trusted custodian calibration HTTP%d: %s", got.Code, got.Body)
			}
			execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('other-read-ws','Other','other-read-ws')`)
			execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('other-read-member','other-read-ws',?,'MEMBER','restricted')`, user)
			got := privateReadSurfaceCall(t, h, ws, user, "MEMBER", surface)
			if got.Code != 200 {
				t.Fatalf("restricted-elsewhere custodian HTTP%d want200: %s", got.Code, got.Body)
			}
			if surface == "document" && strings.Contains(got.Body.String(), `"can_write":true`) {
				t.Error("custody read advertised document write authority")
			}
			if surface == "project-history" && strings.Contains(got.Body.String(), `"restorable":true`) {
				t.Error("custody read advertised project restore authority")
			}
			for _, door := range []string{"document", "project"} {
				r := pagesRequest(t, http.MethodPost, "/private-read/"+door, ws, user, "MEMBER", `{}`)
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindSession))
				r.SetPathValue("slug", "private-read")
				out := httptest.NewRecorder()
				if door == "document" {
					h.MutateDocument(out, r)
				} else {
					r.Method = http.MethodPut
					h.PutProject(out, r)
				}
				if out.Code != 404 {
					t.Errorf("restricted mutation %s HTTP%d want404", door, out.Code)
				}
			}
		})
	}
}

func TestPrivateReadSurfacesDenyStaleOrAbsentReaders(t *testing.T) {
	for _, change := range []string{"suspended", "missing-membership", "foreign-stale-owner"} {
		t.Run(change, func(t *testing.T) {
			h, ws, user, _ := privateReadSurfaceFixture(t)
			switch change {
			case "suspended":
				execOrFatal(t, h.db, `UPDATE users SET suspended_at=CURRENT_TIMESTAMP WHERE id=?`, user)
			case "missing-membership":
				execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, user)
			case "foreign-stale-owner":
				user = "foreign-reader"
				pagesSeedUser(t, h, ws, user, "foreign-reader@example.test", "MEMBER")
			}
			for _, surface := range privateReadSurfaces {
				if got := privateReadSurfaceCall(t, h, ws, user, "OWNER", surface); got.Code != 404 {
					t.Errorf("%s HTTP%d want404: %s", surface, got.Code, got.Body)
				}
			}
		})
	}
}

func TestPrivateReadSurfacesAuditBeforeForeignOwnerOutput(t *testing.T) {
	for _, fault := range []string{"healthy", "abort", "ignore", "delete-row", "rewrite-row"} {
		t.Run(fault, func(t *testing.T) {
			h, ws, _, id := privateReadSurfaceFixture(t)
			pagesSeedUser(t, h, ws, "reader-owner", "reader-owner@example.test", "OWNER")
			switch fault {
			case "abort":
				execOrFatal(t, h.db, `CREATE TRIGGER fail_private_read BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
			case "ignore":
				execOrFatal(t, h.db, `CREATE TRIGGER fail_private_read BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(IGNORE); END`)
			case "delete-row":
				execOrFatal(t, h.db, `CREATE TRIGGER fail_private_read AFTER INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN DELETE FROM audit_logs WHERE id=NEW.id; END`)
			case "rewrite-row":
				execOrFatal(t, h.db, `CREATE TRIGGER fail_private_read AFTER INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN UPDATE audit_logs SET metadata='{}' WHERE id=NEW.id; END`)
			}
			for _, surface := range privateReadSurfaces {
				var before int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND user_id='reader-owner' AND entity_id=?`, id).Scan(&before); err != nil {
					t.Fatal(err)
				}
				got := privateReadSurfaceCall(t, h, ws, "reader-owner", "OWNER", surface)
				want := 200
				if fault != "healthy" {
					want = 500
				}
				if got.Code != want {
					t.Errorf("%s HTTP%d want%d", surface, got.Code, want)
				}
				var after int
				if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND user_id='reader-owner' AND entity_id=?`, id).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if fault == "healthy" && after != before+1 {
					t.Errorf("%s released output without exactly one audit", surface)
				}
				if fault != "healthy" && (after != before || strings.Contains(got.Body.String(), "Private read output")) {
					t.Errorf("%s failed audit retained a row or private metadata", surface)
				}
			}
		})
	}
}

func TestPrivateChatContextCustodyKeepsAgentGrantChecks(t *testing.T) {
	h, ws, issuer := pagesAccessFixture(t)
	execOrFatal(t, h.db, `UPDATE pages SET private_audience=1 WHERE slug='fleet-201'`)
	ctx := withWorkspace(withUser(t.Context(), &AuthUser{ID: "alice"}), ws, "MEMBER")
	resolve := func() error {
		_, err := h.ResolvePageChatContext(ctx, ws, "alice", "agent-watcher", "fleet-201")
		return err
	}
	if err := resolve(); err != nil {
		t.Fatalf("trusted private custody calibration: %v", err)
	}
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('chat-other-ws','Other','chat-other-ws')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('chat-other-member','chat-other-ws','alice','MEMBER','restricted')`)
	if err := resolve(); err != nil {
		t.Errorf("restricted-elsewhere custodian context denied: %v", err)
	}
	ownerCtx := withWorkspace(withUser(t.Context(), &AuthUser{ID: issuer}), ws, "OWNER")
	if _, err := h.ResolvePageChatContext(ownerCtx, ws, issuer, "agent-watcher", "fleet-201"); err != nil {
		t.Fatalf("foreign OWNER context calibration: %v", err)
	}
	var audits int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND user_id=?`, issuer).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Errorf("foreign OWNER context released without exactly one native audit: %d", audits)
	}
	execOrFatal(t, h.db, `CREATE TRIGGER deny_context_audit BEFORE INSERT ON audit_logs WHEN NEW.action='artifact.private_owner_accessed' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if _, err := h.ResolvePageChatContext(ownerCtx, ws, issuer, "agent-watcher", "fleet-201"); err == nil {
		t.Error("foreign OWNER context released on audit failure")
	}
	execOrFatal(t, h.db, `DROP TRIGGER deny_context_audit`)
	execOrFatal(t, h.db, `UPDATE workspace_members SET role='MEMBER' WHERE workspace_id=? AND user_id=?`, ws, issuer)
	if err := resolve(); !errors.Is(err, chatbridge.ErrPageChatAccess) {
		t.Errorf("lost grant issuer authority must deny, got %v", err)
	}
}
