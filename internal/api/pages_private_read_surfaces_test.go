package api

import (
	"context"
	"errors"
	"fmt"
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
	// The agent's grant under test is issued by the workspace OWNER. Since
	// R71 §0.14/1 (#5258) the fixture's agent produce grant comes from alice,
	// the page's owner, because only the owner delegates produce; a workspace
	// OWNER may still grant an agent read, and read is all chat context needs.
	// A produce grant that ends never stands in for read.
	pagesGrant(t, h, ws, issuer, "fleet-201", `{"subject_type":"agent","subject":"watcher","level":"read"}`)
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
	// alice's own produce grant ended with her standing (#5069); the context
	// above came from the OWNER-issued read grant, not from it.
	var aliceLive int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE granted_by_user_id='alice' AND subject_type='agent' AND retired_at IS NULL`).Scan(&aliceLive); err != nil || aliceLive != 0 {
		t.Errorf("alice's agent produce grant outlived her standing: %d %v", aliceLive, err)
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

// What an agent's produce grant means for chat context (R71 §0.14/1, #5258).
// produce opens the page (reachesPage: a producer pushes into panels by name,
// so it must reach the page that names them), so a LIVE produce grant yields
// page context like read does. Only the owner delegates produce; a produce
// grant someone else issued is retired and yields nothing — it is never read
// in disguise. A workspace OWNER/ADMIN may still grant an agent read, and that
// yields context. An owner-issued grant still ends with its issuer's standing
// (#5069), whatever its level, and restoring that standing does not revive it.
// An ownership transfer away from the issuer retires the grant for good. Every
// case holds on an ordinary page and on a private_audience=1 page.
func TestChatContextFollowsAgentGrantDelegation(t *testing.T) {
	// aliceProduceRetired reports whether alice's agent produce grant row is
	// retired, so a denial is pinned to the grant and not to the page.
	aliceProduceRetired := func(t *testing.T, h *PageHandler) bool {
		t.Helper()
		var retired bool
		if err := h.db.QueryRow(`SELECT retired_at IS NOT NULL FROM page_grants WHERE subject_type='agent' AND subject_id='agent-watcher' AND level='produce' AND granted_by_user_id='alice'`).Scan(&retired); err != nil {
			t.Fatalf("alice's agent produce grant row: %v", err)
		}
		return retired
	}
	resolveAs := func(t *testing.T, h *PageHandler, ws, user string) error {
		t.Helper()
		ctx := withWorkspace(withUser(t.Context(), &AuthUser{ID: user}), ws, "MEMBER")
		_, err := h.ResolvePageChatContext(ctx, ws, user, "agent-watcher", "fleet-201")
		return err
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h *PageHandler, ws, issuer string)
		// asker is the human whose chat requests the context; empty is alice.
		asker string
		want  bool
		// then runs after the main assertion, for what must still hold later.
		then func(t *testing.T, h *PageHandler, ws, issuer string)
	}{
		{name: "owner-issued produce yields context", setup: func(t *testing.T, h *PageHandler, _, _ string) {
			if aliceProduceRetired(t, h) {
				t.Fatal("owner-issued agent produce stored retired")
			}
		}, want: true},
		{name: "admin-issued produce is retired and yields none", setup: func(t *testing.T, h *PageHandler, ws, issuer string) {
			execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent' AND subject_id='agent-watcher'`)
			execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) SELECT id,'agent','agent-watcher','produce',? FROM pages WHERE slug='fleet-201'`, issuer)
			var retired bool
			if err := h.db.QueryRow(`SELECT retired_at IS NOT NULL FROM page_grants WHERE subject_type='agent' AND subject_id='agent-watcher' AND level='produce'`).Scan(&retired); err != nil || !retired {
				t.Fatalf("admin-issued agent produce stored live: %v", err)
			}
		}, want: false},
		{name: "admin-issued read yields context", setup: func(t *testing.T, h *PageHandler, ws, issuer string) {
			execOrFatal(t, h.db, `DELETE FROM page_grants WHERE subject_type='agent' AND subject_id='agent-watcher'`)
			pagesGrant(t, h, ws, issuer, "fleet-201", `{"subject_type":"agent","subject":"watcher","level":"read"}`)
		}, want: true},
		{name: "owner-issued produce ends with the owner's standing", setup: func(t *testing.T, h *PageHandler, _, _ string) {
			execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('cc-other','Other','cc-other')`)
			execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('cc-m','cc-other','alice','MEMBER','restricted')`)
			// (a) The denial is for the right reason: the grant row is retired.
			if !aliceProduceRetired(t, h) {
				t.Fatal("alice's agent produce grant outlived her standing (#5069)")
			}
		}, want: false, then: func(t *testing.T, h *PageHandler, ws, _ string) {
			// (b) Lifting the restriction restores alice's standing; retired
			// is for good, so the grant stays retired and yields nothing.
			execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE id='cc-m'`)
			var restricted int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM workspace_members WHERE user_id='alice' AND access_mode='restricted'`).Scan(&restricted); err != nil || restricted != 0 {
				t.Fatalf("alice's standing not restored: %d %v", restricted, err)
			}
			if !aliceProduceRetired(t, h) {
				t.Error("restoring the issuer's standing revived the retired produce grant")
			}
			if err := resolveAs(t, h, ws, "alice"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
				t.Errorf("restored standing revived agent context, got %v", err)
			}
		}},
		{name: "transfer away from the owner retires produce and yields none", setup: func(t *testing.T, h *PageHandler, ws, _ string) {
			// alice keeps #5069 standing as a workspace ADMIN, so only the
			// owner-only trigger (20261109132621) can retire her grant here.
			execOrFatal(t, h.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id='alice'`, ws)
			if aliceProduceRetired(t, h) {
				t.Fatal("calibration: alice's produce grant retired before the transfer")
			}
			var pageID string
			if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='fleet-201'`).Scan(&pageID); err != nil {
				t.Fatal(err)
			}
			pagesSetOwner(t, h, pageID, "dave", "")
			// (d) The previous owner's delegation ends with the transfer.
			if !aliceProduceRetired(t, h) {
				t.Fatal("alice's agent produce grant outlived the ownership transfer")
			}
		}, asker: "dave", want: false, then: func(t *testing.T, h *PageHandler, ws, _ string) {
			if err := resolveAs(t, h, ws, "alice"); !errors.Is(err, chatbridge.ErrPageChatAccess) {
				t.Errorf("former owner still releases agent context, got %v", err)
			}
		}},
	} {
		// (c) Every case holds on an ordinary page and on a private one.
		for _, audience := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/private_audience=%d", tc.name, audience), func(t *testing.T) {
				h, ws, issuer := pagesAccessFixture(t)
				tc.setup(t, h, ws, issuer)
				// After setup: PUT /grants refuses to widen a private
				// audience (409), so grants land first, as in the custody
				// test above; every resolution below runs under the flag.
				execOrFatal(t, h.db, `UPDATE pages SET private_audience=? WHERE slug='fleet-201'`, audience)
				asker := tc.asker
				if asker == "" {
					asker = "alice"
				}
				err := resolveAs(t, h, ws, asker)
				if got := err == nil; got != tc.want {
					t.Fatalf("context released = %v, want %v (err %v)", got, tc.want, err)
				}
				if !tc.want && !errors.Is(err, chatbridge.ErrPageChatAccess) {
					t.Fatalf("denial is not a page chat access denial: %v", err)
				}
				if tc.then != nil {
					tc.then(t, h, ws, issuer)
				}
			})
		}
	}
}
