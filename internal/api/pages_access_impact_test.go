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
