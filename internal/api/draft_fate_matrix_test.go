package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

const (
	draftFateGrant      = `UPDATE page_grants SET level=? WHERE page_id=? AND subject_id=?`
	draftFateFolderUser = `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('fate-folder','user',?,1,?,datetime('now'))`
)

type draftFateCase struct {
	name string
	from string // the grant before the change; "" reaches through the folder ACL
	// to is the grant after: "" removes it. Ignored for folder and private rows.
	to     string
	folder string // "write-to-read" or "read-to-none"
	// private turns on the private audience: a grantee is not its custodian.
	private bool
	pin     bool
	latest  bool // reads the latest content afterwards
	shared  bool // reads the shared version afterwards
	draft   string
}

// The content a person can read decides the draft, and PRD §5 gives Read and
// Comment the same content: "Grantee s Read/Comment dál vidí jen sdílenou verzi".
// Losing Edit therefore loses latest for both, and neither loses read of the Page.
var draftFateCases = []draftFateCase{
	{name: "edit to read, pin", from: "write", to: "read", pin: true, shared: true, draft: "retain"},
	{name: "edit to read, no pin", from: "write", to: "read", draft: "retain"},
	{name: "edit to comment, pin", from: "write", to: "comment", pin: true, shared: true, draft: "retain"},
	{name: "edit to comment, no pin", from: "write", to: "comment", draft: "retain"},
	{name: "read to comment, pin", from: "read", to: "comment", pin: true, shared: true, draft: "retain"},
	{name: "read to comment, no pin", from: "read", to: "comment", draft: "retain"},
	{name: "comment to none, pin", from: "comment", pin: true, draft: "purge"},
	{name: "comment to none, no pin", from: "comment", draft: "purge"},
	{name: "read to none, pin", from: "read", pin: true, draft: "purge"},
	{name: "read to none, no pin", from: "read", draft: "purge"},
	{name: "edit to none, pin", from: "write", pin: true, draft: "purge"},
	{name: "private page, grantee, pin", from: "write", to: "write", private: true, pin: true, draft: "purge"},
	{name: "private page, grantee, no pin", from: "read", to: "read", private: true, draft: "purge"},
	{name: "folder write to read, pin", folder: "write-to-read", pin: true, shared: true, draft: "retain"},
	{name: "folder write to read, no pin", folder: "write-to-read", draft: "retain"},
	{name: "folder read to none, pin", folder: "read-to-none", pin: true, draft: "purge"},
}

// draftFateFolder files the Page in a folder the person reaches. It returns the
// change that takes the row's folder access away.
func draftFateFolder(t *testing.T, db *sql.DB, workspace, page, user, change string) func() {
	t.Helper()
	mustExec(t, db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('fate-crew',?,'Crew','fate-crew')`, workspace)
	mustExec(t, db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('fate-folder',?,'fate-folder','Folder','fate-crew',datetime('now'),datetime('now'))`, workspace)
	mustExec(t, db, draftFateFolderUser, user, change == "write-to-read")
	mustExec(t, db, `UPDATE pages SET folder_id='fate-folder' WHERE id=?`, page)
	return func() {
		if change == "write-to-read" {
			mustExec(t, db, `UPDATE page_folder_acl SET can_write=0 WHERE folder_id='fate-folder' AND subject_id=?`, user)
			return
		}
		mustExec(t, db, `DELETE FROM page_folder_acl WHERE folder_id='fate-folder' AND subject_id=?`, user)
	}
}

func draftFateChange(t *testing.T, db *sql.DB, test draftFateCase, workspace, page, user string) func() {
	t.Helper()
	if test.folder != "" {
		mustExec(t, db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, user)
		return draftFateFolder(t, db, workspace, page, user, test.folder)
	}
	return func() {
		switch {
		case test.private:
			mustExec(t, db, `UPDATE pages SET private_audience=1 WHERE id=?`, page)
		case test.to == "":
			mustExec(t, db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, user)
		default:
			mustExec(t, db, draftFateGrant, test.to, page, user)
		}
	}
}

// Every row is measured through the routes: admission, the preview, the stream's
// retirement finding and the draft-key proof must tell one story.
func TestNativeDraftFateFollowsTheContentThatLostRead(t *testing.T) {
	for _, test := range draftFateCases {
		t.Run(test.name, func(t *testing.T) {
			f, m, d, _, _, _, _ := craftNativeDocumentsFixture(t)
			level := test.from
			if level == "" {
				level = "read"
			}
			before := craftNativePreviewPeer(t, f, level)
			user := UserFromContext(before).ID
			if test.pin {
				allow := func(context.Context, *sql.Tx) (string, error) { return "fixture-owner", nil }
				store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
				if err := store.Share(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, 1, 1, allow); err != nil {
					t.Fatal(err)
				}
			}
			change := draftFateChange(t, f.db, test, f.wsID, "native-page", user)
			before = craftNativeFreshContext(t, f, before, 3600e9)
			change()
			scope := mooring.EditLeaseScope{WorkspaceID: f.wsID, ArtifactID: "native-page"}
			entry := &craftNativeSession{source: *auth.OriginalCredentialFromContext(before), scope: scope}
			after := craftNativeFreshContext(t, f, before, 3600e9)

			if got, _ := m.retirementFinding(after, entry); got != test.draft {
				t.Errorf("stream finding %s, want %s", got, test.draft)
			}
			proof := craftNativeDraftCheck(m, after, user, "native-page")
			if revoked := proof.Code == 403 && strings.Contains(proof.Body.String(), "draft_read_revoked"); revoked != (test.draft == "purge") {
				t.Errorf("draft-key proof %d %s, want draft %s", proof.Code, proof.Body, test.draft)
			}
			wantPreview := 403
			if test.draft == "retain" {
				wantPreview = 404
			}
			if test.shared {
				wantPreview = 200
			}
			if w := craftNativePreviewCall(d, after); w.Code != wantPreview {
				t.Errorf("shared version %d %s, want %d", w.Code, w.Body, wantPreview)
			}
			tx, err := f.db.BeginTx(after, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if view, err := m.pageAuthorityForView(after, tx, *auth.OriginalCredentialFromContext(after), scope, false, true); (err == nil && view.privateRead) != test.latest {
				t.Errorf("latest read %v %v, want %v", view.privateRead, err, test.latest)
			}
		})
	}
}

// Project has the same table, and reaches latest only through Edit.
func TestProjectDraftFateFollowsTheContentThatLostRead(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a", 64))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	for _, test := range draftFateCases {
		t.Run(test.name, func(t *testing.T) {
			h, ws, owner, call := artifactProjectFixture(t)
			created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{"))
			if created.Code != 201 {
				t.Fatalf("create %d %s", created.Code, created.Body)
			}
			if test.pin {
				if got := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); got.Code != 200 {
					t.Fatalf("share %d %s", got.Code, got.Body)
				}
			}
			if got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two")); got.Code != 200 {
				t.Fatalf("v2 %d %s", got.Code, got.Body)
			}
			var page string
			if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
				t.Fatal(err)
			}
			const user = "project-fate-person"
			mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'project-fate@example.com','Person')`, user)
			mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, user)
			level := test.from
			if level == "" {
				level = "read"
			}
			mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,?,?,datetime('now'))`, page, user, level, owner)
			draftFateChange(t, h.db, test, ws, page, user)()

			got := call(user, "GET", "three-screens/artifact", "")
			latest := got.Code == 200 && strings.Contains(got.Body.String(), "Version two")
			shared := got.Code == 200 && strings.Contains(got.Body.String(), "Version one")
			if latest != test.latest || shared != test.shared {
				t.Errorf("read %d latest=%v shared=%v, want latest=%v shared=%v", got.Code, latest, shared, test.latest, test.shared)
			}
			raw, _ := json.Marshal(map[string]string{"human": user, "operation": "wrap", "key": key})
			w := call(user, "POST", "draft-key/"+page, string(raw))
			// A Project stays private until its first share, and a grant does not
			// reach a private audience, so without a pin nobody but the owner reads it.
			want := test.draft
			if !test.pin {
				var private bool
				if err := h.db.QueryRow(`SELECT private_audience FROM pages WHERE id=?`, page).Scan(&private); err != nil || !private {
					t.Fatalf("an unshared Project is private: %v %v", private, err)
				}
				want = "purge"
			}
			if revoked := w.Code == 403 && strings.Contains(w.Body.String(), "draft_read_revoked"); revoked != (want == "purge") || !revoked && w.Code != 200 {
				t.Errorf("draft-key proof %d %s, want draft %s", w.Code, w.Body, want)
			}
		})
	}
}

// Native admission and application eligibility are not read of the Page. The
// stream must answer what draft-key answers, so a person draft-key still lets
// recover the draft never hears "purge".
func TestNativeDraftFateIgnoresNativeAdmissionPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(t *testing.T, db *sql.DB, user string)
	}{
		{name: "restricted private custodian", change: func(t *testing.T, db *sql.DB, user string) {
			mustExec(t, db, `UPDATE pages SET private_audience=1,owner_user_id=? WHERE id='native-page'`, user)
			mustExec(t, db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, user)
		}},
		// The profile row is immutable; without it the Page is simply no longer a
		// native document, and the Page itself stays readable.
		{name: "no native application for the Page", change: func(t *testing.T, db *sql.DB, _ string) {
			mustExec(t, db, `DELETE FROM craft_native_documents WHERE page_id='native-page'`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, m, _, _, _, _, _ := craftNativeDocumentsFixture(t)
			before := craftNativePreviewPeer(t, f, "write")
			user := UserFromContext(before).ID
			before = craftNativeFreshContext(t, f, before, 3600e9)
			test.change(t, f.db, user)
			entry := &craftNativeSession{source: *auth.OriginalCredentialFromContext(before), scope: mooring.EditLeaseScope{WorkspaceID: f.wsID, ArtifactID: "native-page"}}
			after := craftNativeFreshContext(t, f, before, 3600e9)
			if got, _ := m.retirementFinding(after, entry); got != "retain" {
				t.Errorf("stream finding %s, want retain", got)
			}
			if proof := craftNativeDraftCheck(m, after, user, "native-page"); proof.Code != 200 {
				t.Errorf("draft-key %d %s, want 200", proof.Code, proof.Body)
			}
		})
	}
}
