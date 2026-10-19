package api

import (
	"context"
	"testing"
	"time"
)

// Use the actual migration registry. This fixture never creates a substitute
// sharing table; the resolver and coordinated migration must supply the cut.
func privateReaderFixture(t *testing.T) (*PageHandler, *pagesTransferRig, *pageRecord) {
	t.Helper()
	rig := pagesTransferSetup(t)
	rig.seedPage(t, "private-reader-page", "private-reader-page", "Private fixture", rig.userID, "")
	execOrFatal(t, rig.db, `UPDATE pages SET private_audience=1 WHERE id='private-reader-page'`)
	execOrFatal(t, rig.db, `INSERT INTO users(id,email) VALUES('private-reader','private-reader@example.test')`)
	execOrFatal(t, rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('private-reader-member','ptws1','private-reader','MEMBER')`)
	execOrFatal(t, rig.db, `INSERT INTO page_private_readers(page_id,workspace_id,recipient_user_id,recipient_member_id,issuer_user_id,issuer_member_id,issued_at) VALUES('private-reader-page','ptws1','private-reader','private-reader-member','ptdeparting','ptm2','2026-10-19T08:15:00Z')`)
	return NewPageHandler(rig.db, nil, newTestLogger()), rig, &pageRecord{ID: "private-reader-page", Slug: "private-reader-page", OwnerUserID: rig.userID, PrivateAudience: true}
}

func TestPrivateGrantedHumanReadCurrentStanding(t *testing.T) {
	for _, tc := range []struct {
		name, mutation string
		allowed        bool
	}{
		{"explicit custodian grant", "", true},
		{"explicit workspace OWNER grant", `UPDATE page_private_readers SET issuer_user_id='ptadmin',issuer_member_id='ptm1'`, true},
		{"named ADMIN remains a reader", `UPDATE workspace_members SET role='ADMIN' WHERE id='private-reader-member'`, true},
		{"named VIEWER remains a reader", `UPDATE workspace_members SET role='VIEWER' WHERE id='private-reader-member'`, true},
		{"revoked row", `UPDATE page_private_readers SET revoked_at='2026-10-19T08:16:00Z'`, false},
		{"missing explicit row", `DELETE FROM page_private_readers`, false},
		{"Page no longer private", `UPDATE pages SET private_audience=0 WHERE id='private-reader-page'`, false},
		{"custody changed", `UPDATE pages SET owner_user_id='ptadmin' WHERE id='private-reader-page'`, false},
		{"ADMIN issuer no longer custodian", `UPDATE workspace_members SET role='ADMIN' WHERE id='ptm2'; UPDATE pages SET owner_user_id='ptadmin' WHERE id='private-reader-page'`, false},
		{"OWNER issuer demoted", `UPDATE page_private_readers SET issuer_user_id='ptadmin',issuer_member_id='ptm1'; UPDATE workspace_members SET role='ADMIN' WHERE id='ptm1'`, false},
		{"issuer suspended", `UPDATE users SET suspended_at='2026-10-19T08:16:00Z' WHERE id='ptdeparting'`, false},
		{"recipient suspended", `UPDATE users SET suspended_at='2026-10-19T08:16:00Z' WHERE id='private-reader'`, false},
		{"issuer restricted here", `UPDATE workspace_members SET access_mode='restricted' WHERE id='ptm2'`, false},
		{"recipient restricted here", `UPDATE workspace_members SET access_mode='restricted' WHERE id='private-reader-member'`, false},
		{"issuer membership belongs to another human", `UPDATE page_private_readers SET issuer_member_id='ptm1'`, false},
		{"recipient membership belongs to another human", `UPDATE page_private_readers SET recipient_member_id='ptm1'`, false},
		{"issuer removed and rejoined", `DELETE FROM workspace_members WHERE id='ptm2'; INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('replacement-issuer','ptws1','ptdeparting','MEMBER')`, false},
		{"recipient removed and rejoined", `DELETE FROM workspace_members WHERE id='private-reader-member'; INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('replacement-reader','ptws1','private-reader','MEMBER')`, false},
		{"workspace retired", `UPDATE workspaces SET deleted_at='2026-10-19T08:16:00Z' WHERE id='ptws1'`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, rig, rec := privateReaderFixture(t)
			if tc.mutation != "" {
				execOrFatal(t, rig.db, tc.mutation)
			}
			allowed, err := h.privateGrantedHumanRead(t.Context(), rig.wsID, rec, "private-reader")
			if err != nil || allowed != tc.allowed {
				t.Fatalf("allowed=%v want=%v err=%v", allowed, tc.allowed, err)
			}
		})
	}
}

func TestPrivateGrantedHumanReadRestrictedAnywhere(t *testing.T) {
	for _, user := range []string{"private-reader", "ptdeparting"} {
		t.Run(user, func(t *testing.T) {
			h, rig, rec := privateReaderFixture(t)
			execOrFatal(t, rig.db, `INSERT INTO workspaces(id,name,slug) VALUES('other-reader-workspace','Other','other-reader-workspace')`)
			execOrFatal(t, rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('other-reader-member','other-reader-workspace',?,'MEMBER','restricted')`, user)
			allowed, err := h.privateGrantedHumanRead(t.Context(), rig.wsID, rec, "private-reader")
			if err != nil || allowed {
				t.Fatalf("global restricted ceiling ignored: allowed=%v err=%v", allowed, err)
			}
		})
	}
}

func TestPrivateGrantedHumanReadScopeAndIntrinsicSeparation(t *testing.T) {
	h, rig, original := privateReaderFixture(t)
	for _, name := range []string{"nil Page", "empty Page", "nonprivate record", "missing custodian", "mixed ownership", "foreign Page", "foreign workspace", "empty workspace", "empty human", "custodian without explicit row", "OWNER without explicit row"} {
		t.Run(name, func(t *testing.T) {
			copy := *original
			rec, workspace, human := &copy, rig.wsID, "private-reader"
			switch name {
			case "nil Page":
				rec = nil
			case "empty Page":
				rec.ID = ""
			case "nonprivate record":
				rec.PrivateAudience = false
			case "missing custodian":
				rec.OwnerUserID = ""
			case "mixed ownership":
				rec.OwnerCrewID = "unexpected-owner-crew"
			case "foreign Page":
				rec.ID = "another-page"
			case "foreign workspace":
				workspace = "another-workspace"
			case "empty workspace":
				workspace = ""
			case "empty human":
				human = ""
			case "custodian without explicit row":
				human = rig.userID
			case "OWNER without explicit row":
				human = rig.adminID
			}
			allowed, err := h.privateGrantedHumanRead(t.Context(), workspace, rec, human)
			if err != nil || allowed {
				t.Fatalf("scope/intrinsic check widened named read: allowed=%v err=%v", allowed, err)
			}
		})
	}
}

func TestPrivateGrantedHumanReadIgnoresLegacyGrants(t *testing.T) {
	h, rig, rec := privateReaderFixture(t)
	execOrFatal(t, rig.db, `DELETE FROM page_private_readers`)
	rig.seedCrew(t, "reader-crew", "Reader crew", "reader-crew")
	rig.seedCrewMember(t, "reader-crew", "private-reader")
	for _, subject := range []struct{ kind, id string }{{"user", "private-reader"}, {"crew", "reader-crew"}, {"workspace", ""}} {
		execOrFatal(t, rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('private-reader-page',?,?,'read','ptdeparting','2026-10-19T08:15:00Z')`, subject.kind, subject.id)
	}
	allowed, err := h.privateGrantedHumanRead(t.Context(), rig.wsID, rec, "private-reader")
	if err != nil || allowed {
		t.Fatalf("legacy grant activated private read: allowed=%v err=%v", allowed, err)
	}
}

func TestPrivateGrantedHumanReadUsesTransactionReader(t *testing.T) {
	h, rig, rec := privateReaderFixture(t)
	rig.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := rig.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE page_private_readers SET revoked_at='2026-10-19T08:16:00Z'`); err != nil {
		t.Fatal(err)
	}
	allowed, err := h.privateGrantedHumanRead(context.WithValue(ctx, pageAuthReaderKey{}, tx), rig.wsID, rec, "private-reader")
	if err != nil || allowed || ctx.Err() != nil {
		t.Fatalf("resolver escaped current transaction: allowed=%v err=%v context=%v", allowed, err, ctx.Err())
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	allowed, err = h.privateGrantedHumanRead(t.Context(), rig.wsID, rec, "private-reader")
	if err != nil || !allowed {
		t.Fatalf("rollback did not retain original grant: allowed=%v err=%v", allowed, err)
	}
}

func TestPrivateGrantedHumanReadMissingStorageFailsClosed(t *testing.T) {
	h, rig, rec := privateReaderFixture(t)
	execOrFatal(t, rig.db, `DROP TABLE page_private_readers`)
	allowed, err := h.privateGrantedHumanRead(t.Context(), rig.wsID, rec, "private-reader")
	if err == nil || allowed {
		t.Fatalf("missing explicit-share storage fell open: allowed=%v err=%v", allowed, err)
	}
}
