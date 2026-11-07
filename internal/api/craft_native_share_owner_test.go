package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// craftNativeShareState is what a share attempt can change.
func craftNativeShareState(t *testing.T, s craftNativeSharedOnly) (pin, audits, entries int) {
	t.Helper()
	if err := s.f.db.QueryRow(`SELECT v.shared_version,(SELECT count(*) FROM audit_logs WHERE action='craft.native_preview_shared'),(SELECT count(*) FROM journal_entries WHERE entry_type='craft.native_preview_shared') FROM artifact_version_heads v WHERE v.page_id='native-page'`).Scan(&pin, &audits, &entries); err != nil {
		t.Fatal(err)
	}
	return pin, audits, entries
}

// craftNativeFreeLease ends the fixture owner's custody. Call it before changing
// access: an access change retires the owner's session, and with it the release.
func craftNativeFreeLease(t *testing.T, s craftNativeSharedOnly) {
	t.Helper()
	if w := craftNativeLeaseCall(s.h, s.owner, s.token, "native-page", "release", `{"generation":"1"}`); w.Code != 200 {
		t.Fatalf("release: %d %s", w.Code, w.Body)
	}
}

// craftNativeHolder gives ctx the free lease and returns a share request for
// the latest version, under that session's own generation.
func craftNativeHolder(t *testing.T, s craftNativeSharedOnly, ctx context.Context) (string, craftNativeShareBody) {
	t.Helper()
	token, id := craftNativeIssued(t, s.h, ctx)
	acquired := craftNativeLeaseCall(s.h, ctx, token, "native-page", "acquire", `{}`)
	var lease struct {
		Generation string `json:"generation"`
		Mine       bool   `json:"mine"`
		CanWrite   bool   `json:"can_write"`
	}
	if acquired.Code != 200 || json.Unmarshal(acquired.Body.Bytes(), &lease) != nil || !lease.Mine || !lease.CanWrite {
		t.Fatalf("calibration: this person must hold a writable lease: %d %s", acquired.Code, acquired.Body)
	}
	return token, craftNativeShareBody{WorkspaceID: s.f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: craftNativeEnginePin(s.d.applications["wordcraft"].Store), SessionID: id, Generation: lease.Generation, BaseVersion: "3", TargetVersion: "3"}
}

func craftNativeEnginePin(cfg mooring.NativeStoreConfig) string {
	return mooring.NewNativeStore(nil, cfg).EnginePin()
}

// PRD §5: "sdílet verzi smí jen vlastník". Each person below really holds the
// lease and can save, so the only thing left that refuses them is the rule.
func TestCraftNativeShareIsRefusedToEveryoneButThePageOwner(t *testing.T) {
	member := func(s craftNativeSharedOnly, id, role, grant string) context.Context {
		return craftNativeVersionsMember(t, s.f, id, id, role, grant)
	}
	for _, tc := range []struct {
		name    string
		private bool
		person  func(*testing.T, craftNativeSharedOnly) context.Context
	}{
		{"edit grantee", false, func(_ *testing.T, s craftNativeSharedOnly) context.Context {
			return member(s, "share-edit", "MEMBER", "write")
		}},
		{"everyone with edit", false, func(_ *testing.T, s craftNativeSharedOnly) context.Context {
			ctx := member(s, "share-everyone", "MEMBER", "")
			mustExec(t, s.f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-page','workspace','','write',?)`, s.f.userID)
			return ctx
		}},
		{"folder editor", false, func(_ *testing.T, s craftNativeSharedOnly) context.Context {
			ctx := member(s, "share-folder", "MEMBER", "")
			mustExec(t, s.f.db, `INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('share-crew',?,'Share','share','free')`, s.f.wsID)
			mustExec(t, s.f.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('share-folder-row',?,'share-folder','Folder','share-crew',datetime('now'),datetime('now'))`, s.f.wsID)
			mustExec(t, s.f.db, `UPDATE pages SET folder_id='share-folder-row' WHERE id='native-page'`)
			mustExec(t, s.f.db, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_read,can_write,set_at) VALUES('share-folder-row','user','share-folder',1,1,datetime('now'))`)
			return ctx
		}},
		{"workspace admin", false, func(_ *testing.T, s craftNativeSharedOnly) context.Context {
			return member(s, "share-admin", "ADMIN", "")
		}},
		{"workspace owner", false, func(_ *testing.T, s craftNativeSharedOnly) context.Context {
			return member(s, "share-owner", "OWNER", "")
		}},
		{"workspace owner on a private page", true, func(_ *testing.T, s craftNativeSharedOnly) context.Context {
			return member(s, "share-private-owner", "OWNER", "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := craftNativeSharedOnlyFixture(t)
			craftNativeFreeLease(t, s)
			if tc.private {
				mustExec(t, s.f.db, `UPDATE pages SET private_audience=1 WHERE id='native-page'`)
			}
			ctx := tc.person(t, s)
			token, share := craftNativeHolder(t, s, ctx)
			pin, audits, entries := craftNativeShareState(t, s)
			w := craftNativeShareCall(t, s.d, ctx, token, share)
			if w.Code != 403 || !strings.Contains(w.Body.String(), "only the page owner") {
				t.Fatalf("non-owner share: %d %s", w.Code, w.Body)
			}
			if p, a, e := craftNativeShareState(t, s); p != pin || a != audits || e != entries {
				t.Fatalf("refused share changed state: pin %d->%d audit %d->%d journal %d->%d", pin, p, audits, a, entries, e)
			}
			var view struct {
				CanShare bool `json:"can_share"`
				CanEdit  bool `json:"can_edit"`
			}
			if got := craftNativeLeaseCall(s.h, ctx, token, "native-page", "read", `{}`); got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &view) != nil || view.CanShare || !view.CanEdit {
				t.Fatalf("lease must keep edit and deny share: %d %s", got.Code, got.Body)
			}
		})
	}
}

// The owner's own share keeps working and keeps its audit, with the owner as
// the recorded actor, and the lease tells the UI to offer it.
func TestCraftNativeOwnerSharesAndIsAudited(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	craftNativeFreeLease(t, s)
	token, share := craftNativeHolder(t, s, s.owner)
	var view struct {
		CanShare bool `json:"can_share"`
	}
	if got := craftNativeLeaseCall(s.h, s.owner, token, "native-page", "read", `{}`); got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &view) != nil || !view.CanShare {
		t.Fatalf("owner lease must offer share: %d %s", got.Code, got.Body)
	}
	// An agent run carrying the owner's credential is not the owner.
	run := context.WithValue(s.owner, artifactRunKey{}, &artifactRunAuthority{RunID: "share-run", AgentID: "share-agent", WorkspaceID: s.f.wsID, HumanUserID: s.f.userID})
	if w := craftNativeShareCall(t, s.d, run, token, share); w.Code != 403 {
		t.Fatalf("agent run shared: %d %s", w.Code, w.Body)
	}
	if w := craftNativeShareCall(t, s.d, s.owner, token, share); w.Code != 200 {
		t.Fatalf("owner share: %d %s", w.Code, w.Body)
	}
	pin, audits, entries := craftNativeShareState(t, s)
	if pin != 3 || audits != 2 || entries != 2 {
		t.Fatalf("owner share pin/audit/journal %d/%d/%d", pin, audits, entries)
	}
	var actor string
	if err := s.f.db.QueryRow(`SELECT user_id FROM audit_logs WHERE action='craft.native_preview_shared' ORDER BY created_at DESC, rowid DESC LIMIT 1`).Scan(&actor); err != nil || actor != s.f.userID {
		t.Fatalf("audit actor %q: %v", actor, err)
	}
}

// The check sits in the transaction that moves the pin: losing ownership at the
// last moment rolls the pin and its audit back, it does not arrive after them.
func TestCraftNativeShareOwnerCheckRunsInThePinTransaction(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	craftNativeFreeLease(t, s)
	token, share := craftNativeHolder(t, s, s.owner)
	pin, audits, entries := craftNativeShareState(t, s)
	mustExec(t, s.f.db, `CREATE TRIGGER native_share_owner_moves_after_pin AFTER UPDATE OF shared_version ON artifact_version_heads BEGIN UPDATE pages SET owner_user_id='`+s.f.adminID+`' WHERE id='native-page'; END`)
	if w := craftNativeShareCall(t, s.d, s.owner, token, share); w.Code != 403 {
		t.Fatalf("share after losing ownership: %d %s", w.Code, w.Body)
	}
	if p, a, e := craftNativeShareState(t, s); p != pin || a != audits || e != entries {
		t.Fatalf("rolled back share left pin %d->%d audit %d->%d journal %d->%d", pin, p, audits, a, entries, e)
	}
}

// Ownership moved to a crew when the human owner left. No person is named, so
// nobody shares until a person owns the page again. The Project and Design
// paths behave the same way. Whether a crew member should count as the owner
// is the owner's decision, recorded in the close-out report.
func TestCraftNativeCrewCustodyHasNoSharer(t *testing.T) {
	s := craftNativeSharedOnlyFixture(t)
	craftNativeFreeLease(t, s)
	ctx := craftNativeVersionsMember(t, s.f, "share-crew-member", "Crew member", "MEMBER", "")
	mustExec(t, s.f.db, `INSERT INTO crews(id,workspace_id,name,slug,network_mode) VALUES('custody-crew',?,'Custody','custody','free')`, s.f.wsID)
	mustExec(t, s.f.db, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('custody-member','custody-crew','share-crew-member')`)
	mustExec(t, s.f.db, `UPDATE pages SET owner_user_id=NULL,owner_crew_id='custody-crew' WHERE id='native-page'`)
	token, id := craftNativeIssued(t, s.h, ctx)
	acquired := craftNativeLeaseCall(s.h, ctx, token, "native-page", "acquire", `{}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if acquired.Code != 200 || json.Unmarshal(acquired.Body.Bytes(), &lease) != nil {
		t.Fatalf("calibration: a crew member edits a crew-owned page: %d %s", acquired.Code, acquired.Body)
	}
	share := craftNativeShareBody{WorkspaceID: s.f.wsID, PageID: "native-page", ApplicationID: "wordcraft", EnginePin: craftNativeEnginePin(s.d.applications["wordcraft"].Store), SessionID: id, Generation: lease.Generation, BaseVersion: "3", TargetVersion: "3"}
	if w := craftNativeShareCall(t, s.d, ctx, token, share); w.Code != 403 {
		t.Fatalf("crew member shared: %d %s", w.Code, w.Body)
	}
}
