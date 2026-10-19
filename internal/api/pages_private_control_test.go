package api

import (
	"context"
	"testing"
)

func privateControlFixture(t *testing.T) (*PageHandler, string, *pageRecord, string, string, string) {
	t.Helper()
	rig := pagesTransferSetup(t)
	h := NewPageHandler(rig.db, nil, newTestLogger())
	const page = "private-control"
	rig.seedPage(t, page, page, "Private control fixture", rig.userID, "")
	execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
	const admin = "private-control-admin"
	pagesSeedUser(t, h, rig.wsID, admin, "private-control-admin@example.test", "ADMIN")
	return h, rig.wsID, &pageRecord{ID: page, Slug: page, OwnerUserID: rig.userID, PrivateAudience: true}, rig.userID, rig.adminID, admin
}

// Call the control predicates directly: private metadata is still hidden from
// other roles by the existing loader. A future read-only invitation must not
// turn that visibility into grant administration or production authority.
func TestPrivatePageControlIntrinsicAuthority(t *testing.T) {
	h, workspace, rec, author, owner, admin := privateControlFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, user, role string
		allowed          bool
	}{
		{"custodian", author, "MEMBER", true},
		{"workspace OWNER", owner, "OWNER", true},
		{"other ADMIN", admin, "ADMIN", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := h.mayAdministerGrants(ctx, workspace, tc.user, tc.role, rec); got != tc.allowed {
				t.Errorf("private grant administration=%v want%v", got, tc.allowed)
			}
			for _, kind := range []string{"script", "webhook"} {
				allowed, reason := h.mayProduce(ctx, workspace, tc.user, tc.role, rec, &panelRecord{PanelID: "fixture", ProducerKind: kind})
				if allowed != tc.allowed || (!allowed && reason == "") {
					t.Errorf("private %s production=%v reason=%q want%v", kind, allowed, reason, tc.allowed)
				}
			}
			// These shared intrinsic predicates must remain unchanged.
			if privatePageViewer(rec, &pageViewer{UserID: tc.user, Role: tc.role}) != tc.allowed ||
				h.mayEditSpec(ctx, workspace, tc.user, tc.role, rec) != tc.allowed {
				t.Error("private viewer/editor intrinsic boundary changed")
			}
			// Stronger agent/routine producer identity is not bypassed even by
			// custodians or OWNER; no explicit producer grant exists here.
			for _, kind := range []string{"agent", "routine"} {
				if allowed, _ := h.mayProduce(ctx, workspace, tc.user, tc.role, rec, &panelRecord{PanelID: "fixture", ProducerKind: kind}); allowed {
					t.Errorf("%s producer identity bypassed", kind)
				}
			}
		})
	}
}

func TestPrivatePageControlLegacyProduceDoesNotElevate(t *testing.T) {
	h, workspace, rec, author, _, admin := privateControlFixture(t)
	ctx := context.Background()
	// Deliberately seed an ordinary legacy grant. It has no explicit private
	// sharing provenance, even though its issuer still owns this Page.
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id) VALUES(?,'user',?,'produce','["fixture"]',?)`, rec.ID, admin, author)
	viewer, err := h.loadViewer(ctx, workspace, admin)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := h.grantsFor(ctx, workspace, rec, viewer)
	if err != nil || len(grants) != 1 || grants[0].Level != pageGrantProduce || !grants[0].covers("fixture") {
		t.Fatalf("fixture must exercise an otherwise live legacy produce grant: %#v %v", grants, err)
	}
	for _, kind := range []string{"script", "webhook", "agent", "routine"} {
		if allowed, reason := h.mayProduce(ctx, workspace, admin, "ADMIN", rec, &panelRecord{PanelID: "fixture", ProducerKind: kind}); allowed || reason == "" {
			t.Errorf("legacy grant elevated private %s producer: allowed=%v reason=%q", kind, allowed, reason)
		}
	}
	// Ordinary non-private grant semantics are preserved, including named
	// agent/routine panels: the change must not disable produce grants globally.
	public := *rec
	public.PrivateAudience = false
	for _, kind := range []string{"agent", "routine"} {
		if allowed, reason := h.mayProduce(ctx, workspace, admin, "ADMIN", &public, &panelRecord{PanelID: "fixture", ProducerKind: kind}); !allowed {
			t.Errorf("ordinary %s grant rejected: %s", kind, reason)
		}
	}
}

func TestPrivatePageControlPublicManagementUnchanged(t *testing.T) {
	h, workspace, rec, _, _, admin := privateControlFixture(t)
	public := *rec
	public.PrivateAudience = false
	ctx := context.Background()
	if !h.mayAdministerGrants(ctx, workspace, admin, "ADMIN", &public) {
		t.Fatal("ordinary ADMIN grant management changed")
	}
	for _, kind := range []string{"script", "webhook"} {
		if allowed, reason := h.mayProduce(ctx, workspace, admin, "ADMIN", &public, &panelRecord{PanelID: "fixture", ProducerKind: kind}); !allowed {
			t.Errorf("ordinary ADMIN %s production rejected: %s", kind, reason)
		}
	}
}
