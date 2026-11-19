package api

import (
	"encoding/json"
	"testing"
)

func TestPageAccessEffectiveLevelsAndFolderInheritance(t *testing.T) {
	f := newFoldersFixture(t)
	f.createFolder(t, "engine", "Engine", "crew/engine")
	f.share(t, "engine", "user", "frank", true)
	f.file(t, "engine", "fleet-201")
	pagesGrant(t, f.h, f.wsID, f.owner, "fleet-201", `{"subject_type":"user","subject":"dave","level":"comment"}`)
	w := pagesAccessCall(t, f.h, f.wsID, "alice", "MEMBER", "fleet-201", "")
	expectStatus(t, w, 200)
	var doc pageAccessWire
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.PageID == "" || doc.PagesVersion != f.pagesVersion(t, "fleet-201") {
		t.Fatal("missing immutable identity/fence")
	}
	seen := map[string]pageAccessSubjectWire{}
	for _, s := range doc.Subjects {
		if s.SubjectType == "user" {
			seen[s.SubjectID] = s
		}
	}
	if seen["dave"].EffectivePermissions != (pageAccessPermissions{Read: true, Comment: true}) {
		t.Fatalf("commenter %+v", seen["dave"])
	}
	if seen["frank"].EffectivePermissions != (pageAccessPermissions{Read: true, Comment: true, Edit: true}) {
		t.Fatalf("folder editor %+v", seen["frank"])
	}
	if len(seen["frank"].Sources) != 1 || !seen["frank"].Sources[0].Inherited {
		t.Fatal("missing inherited source")
	}
	f.unshare(t, "engine", "user", "frank")
	w = pagesAccessCall(t, f.h, f.wsID, "alice", "MEMBER", "fleet-201", "")
	expectStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Subjects {
		if s.SubjectID == "frank" {
			t.Fatal("revoked inherited access survived")
		}
	}
}

func TestPageAccessProduceDoesNotBecomeCommentOrEdit(t *testing.T) {
	subjects := []pageAccessSubjectWire{{SubjectType: "user", SubjectID: "producer", Paths: []string{pageReachGrantPage + pageGrantProduce}}}
	decoratePageAccess(subjects, &pageRecord{}, &pageAccessWorkspace{}, nil)
	if subjects[0].EffectivePermissions != (pageAccessPermissions{Read: true, Produce: true}) {
		t.Fatalf("produce expanded: %+v", subjects[0])
	}
}

func TestPageAccessRejectsStaleAdministratorContext(t *testing.T) {
	f := newFoldersFixture(t)
	expectStatus(t, pagesAccessCall(t, f.h, f.wsID, "alice", "MEMBER", "fleet-201", ""), 200)
	// The middleware role was ADMIN, but the current membership in the read
	// transaction is MEMBER. A direct read grant is not ACL administration.
	w := pagesAccessCall(t, f.h, f.wsID, "dave", "ADMIN", "fleet-201", "")
	expectStatus(t, w, 403)
}

// A private artifact's role path is the administrator READ rule (#4825/#4839):
// an ADMIN reaches it to read with audit, never to comment or edit. Only the
// workspace OWNER, as custodian, keeps the write/administration audience. The
// access report and the impact preview share this decoration, so both must say
// what the server enforces (artifact_admin_read_test.go).
func TestPageAccessPrivateRolePathMatchesCustodianRule(t *testing.T) {
	rec := &pageRecord{PrivateAudience: true, OwnerUserID: "author"}
	ws := &pageAccessWorkspace{Members: []pageAccessMember{
		{ID: "author", Role: "MEMBER"},
		{ID: "admin", Role: "ADMIN"},
		{ID: "owner", Role: "OWNER"},
	}}
	subjects := []pageAccessSubjectWire{
		{SubjectType: pageSubjectUser, SubjectID: "author", Paths: []string{pageReachOwner}},
		{SubjectType: pageSubjectUser, SubjectID: "admin", Paths: []string{pageReachRole}},
		{SubjectType: pageSubjectUser, SubjectID: "owner", Paths: []string{pageReachRole}},
	}
	decoratePageAccess(subjects, rec, ws, nil)
	want := map[string]pageAccessPermissions{
		"author": {Read: true, Comment: true, Edit: true},
		"admin":  {Read: true},
		"owner":  {Read: true, Comment: true, Edit: true},
	}
	for _, s := range subjects {
		if s.EffectivePermissions != want[s.SubjectID] {
			t.Errorf("%s: effective %+v, want %+v", s.SubjectID, s.EffectivePermissions, want[s.SubjectID])
		}
		if len(s.Sources) != 1 || s.Sources[0].Permissions != want[s.SubjectID] {
			t.Errorf("%s: sources %+v", s.SubjectID, s.Sources)
		}
	}

	// A shared page keeps the manage role's edit path unchanged.
	shared := []pageAccessSubjectWire{{SubjectType: pageSubjectUser, SubjectID: "admin", Paths: []string{pageReachRole}}}
	decoratePageAccess(shared, &pageRecord{OwnerUserID: "author"}, ws, nil)
	if shared[0].EffectivePermissions != (pageAccessPermissions{Read: true, Comment: true, Edit: true}) {
		t.Fatalf("shared page admin: %+v", shared[0].EffectivePermissions)
	}
}
