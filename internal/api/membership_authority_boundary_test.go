package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// A trusted membership change no longer moves users.authority_revision, so a
// credential captured before it still matches. These rows prove what replaced
// that fence: every old right is gone after each change, not only the Native
// session. Stored authority (receipt, run principal, stream ticket) must not
// survive a change in its own workspace. A credential consumer must never hold
// more than a credential taken now, and it must lose what the change removed.
type boundaryConsumers struct {
	f       *pagesTransferRig
	native  *craftNativeHTTP
	pa      *artifactPrincipalFixture
	receipt *chataudience.Receipt
	source  *auth.OriginalCredential
	claims  *auth.Claims
	mw      *AuthMiddleware
}

func (b *boundaryConsumers) receiptHeld(t *testing.T) bool {
	t.Helper()
	current, err := chataudience.RevalidateOriginalUserQuery(t.Context(), b.f.db, b.receipt)
	if err != nil {
		t.Fatal(err)
	}
	return current != nil
}

// The pair pages_storage_upload.go runs for an agent before it reserves bytes.
func (b *boundaryConsumers) runUploadHeld(t *testing.T) bool {
	t.Helper()
	if b.pa.pages.checkOriginalUserArtifactRunPrincipal(t.Context(), &b.pa.a) != nil {
		return false
	}
	_, err := b.pa.pages.validateArtifactPrincipalOriginalCredential(t.Context(), &b.pa.a, "pages:write")
	return err == nil
}

// stream_credentials.go admits the ticket; the socket then captures a receipt
// from it. The old authority survives only if that receipt is the old one.
func (b *boundaryConsumers) ticketHeld(t *testing.T) bool {
	t.Helper()
	ctx, release, err := b.mw.authenticateStream(t.Context(), b.claims)
	if err != nil {
		return false
	}
	defer release()
	h := NewInternalHandler(b.f.db, "synthetic-master", newTestLogger())
	receipt, err := h.originalHumanChatReceipt(ctx, b.receipt.ChatID, b.receipt.UserID, nil, auth.OriginalCredentialFromContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	return receipt != nil && *receipt == *b.receipt
}

func (b *boundaryConsumers) humanUploadHeld(t *testing.T, ctx context.Context) bool {
	t.Helper()
	tx, err := b.f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
	_, err = b.pa.pages.artifactCreationUploadAuthority(r, b.f.wsID)(ctx, tx)
	return err == nil
}

func (b *boundaryConsumers) nativeWriteHeld(t *testing.T, source auth.OriginalCredential) bool {
	t.Helper()
	tx, err := b.f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = b.native.pageAuthority(t.Context(), tx, source, mooring.EditLeaseScope{WorkspaceID: b.f.wsID, ArtifactID: "native-page"}, true)
	return err == nil
}

func (b *boundaryConsumers) generation(t *testing.T, workspace string) string {
	t.Helper()
	var revision string
	if err := b.f.db.QueryRow(`SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind='user' AND scope_id=?`, workspace, b.f.userID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

func newBoundaryConsumers(t *testing.T, seed func(*pagesTransferRig)) (*boundaryConsumers, context.Context) {
	t.Helper()
	f, native, _ := craftNativeHTTPFixture(t)
	// MANAGER creates by role, so a demotion has something real to remove.
	mustExec(t, f.db, `UPDATE workspace_members SET role='MANAGER' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
	if seed != nil {
		seed(f)
	}
	ctx := craftNativeFreshRequestContext(t, f, f.userID, "native-source")
	source := auth.OriginalCredentialFromContext(ctx)
	pa := newArtifactPrincipalFixtureWithRig(t, f)
	h := NewInternalHandler(f.db, "synthetic-master", newTestLogger())
	receipt, err := h.originalHumanChatReceipt(t.Context(), pa.a.ChatID, f.userID, nil, source)
	if err != nil || receipt == nil {
		t.Fatal("calibration receipt", receipt, err)
	}
	pa.receipt = receipt
	pa.admit(t)
	v, err := auth.NewJWTValidator("boundary-source-test-secret-at-least-32-chars")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := v.IssueOriginalCredentialWSTicket(source.UserID, source.ID, "", "", "", *source)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.ValidateWS(ticket)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.db, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES(?,'user',?)`, f.wsID, f.userID)
	return &boundaryConsumers{f: f, native: native, pa: pa, receipt: receipt, source: source, claims: claims, mw: NewAuthMiddleware(v, sessions.NewDBStore(f.db), f.db, newTestLogger())}, ctx
}

func TestMembershipChangeLeavesNoOldRight(t *testing.T) {
	const own = `UPDATE workspace_members SET role=? WHERE user_id='ptdeparting' AND workspace_id='ptws1'`
	otherMember := func(role, mode string) func(*pagesTransferRig) {
		return func(f *pagesTransferRig) {
			mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('boundary-other','Other','boundary-other')`)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('boundary-other-member','boundary-other',?,?,?)`, f.userID, role, mode)
		}
	}
	type change = func(t *testing.T, f *pagesTransferRig)
	for _, test := range []struct {
		name      string
		seed      func(*pagesTransferRig)
		mutate    change
		unrelated bool // Nothing about this workspace changed: everything stays.
		noCreate  bool // The human's right to create must be gone afterwards.
		noWrite   bool // The human's right to write the Page must be gone.
		noEpoch   bool // Capabilities are not a generation input, here or on main.
	}{
		{name: "role down", noCreate: true, mutate: func(t *testing.T, f *pagesTransferRig) { mustExec(t, f.db, own, "MEMBER") }},
		{name: "role down and back up", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, own, "MEMBER")
			mustExec(t, f.db, own, "MANAGER")
		}},
		{name: "capability added and removed", noEpoch: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET capabilities='["pages:read"]' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
			mustExec(t, f.db, `UPDATE workspace_members SET capabilities=NULL WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
		}},
		{name: "removal", noCreate: true, noWrite: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
		}},
		{name: "removal and restoration as the product does it", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('boundary-fresh-member',?,?,'MANAGER')`, f.wsID, f.userID)
		}},
		{name: "removal and restoration with the same member id", mutate: func(t *testing.T, f *pagesTransferRig) {
			var id string
			if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id=?`, id)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MANAGER')`, id, f.wsID, f.userID)
		}},
		// access_revision restarts at 1 on a new row. One promotion brings it back
		// to the value in the old receipt: member id, revision and role all match.
		{name: "same member id restored at the receipt's old revision", mutate: func(t *testing.T, f *pagesTransferRig) {
			var id string
			if err := f.db.QueryRow(`SELECT id FROM workspace_members WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID).Scan(&id); err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id=?`, id)
			mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, id, f.wsID, f.userID)
			mustExec(t, f.db, `UPDATE workspace_members SET role='MANAGER' WHERE id=?`, id)
		}},
		// A workspace backup restored over a removal: dbdump.go inserts the dumped row
		// with its id and access_revision. Reachable from the product, unlike the row above.
		{name: "same member row brought back by a backup restore", mutate: func(t *testing.T, f *pagesTransferRig) {
			var id string
			var revision int64
			if err := f.db.QueryRow(`SELECT id,access_revision FROM workspace_members WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID).Scan(&id, &revision); err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id=?`, id)
			mustExec(t, f.db, `INSERT OR IGNORE INTO workspace_members(id,workspace_id,user_id,role,access_revision) VALUES(?,?,?,'MANAGER',?)`, id, f.wsID, f.userID, revision)
		}},
		{name: "restriction here", noCreate: true, noWrite: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
		}},
		{name: "restriction here, then lifted", mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='trusted' WHERE user_id=? AND workspace_id=?`, f.userID, f.wsID)
		}},
		{name: "restricted row added elsewhere", noCreate: true, noWrite: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			otherMember("MEMBER", "restricted")(f)
		}},
		{name: "restricted row added elsewhere, then removed", mutate: func(t *testing.T, f *pagesTransferRig) {
			otherMember("MEMBER", "restricted")(f)
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id='boundary-other-member'`)
		}},
		{name: "restriction elsewhere, then lifted", seed: otherMember("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='restricted' WHERE id='boundary-other-member'`)
			mustExec(t, f.db, `UPDATE workspace_members SET access_mode='trusted' WHERE id='boundary-other-member'`)
		}},
		{name: "control: trusted role change elsewhere", unrelated: true, seed: otherMember("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `UPDATE workspace_members SET role='ADMIN' WHERE id='boundary-other-member'`)
		}},
		{name: "control: leaving a trusted workspace elsewhere", unrelated: true, seed: otherMember("MEMBER", "trusted"), mutate: func(t *testing.T, f *pagesTransferRig) {
			mustExec(t, f.db, `DELETE FROM workspace_members WHERE id='boundary-other-member'`)
		}},
		{name: "control: joining another workspace", unrelated: true, mutate: func(t *testing.T, f *pagesTransferRig) {
			otherMember("OWNER", "trusted")(f)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, ctx := newBoundaryConsumers(t, test.seed)
			for name, held := range map[string]bool{"receipt": b.receiptHeld(t), "run upload": b.runUploadHeld(t), "stream ticket": b.ticketHeld(t), "human upload": b.humanUploadHeld(t, ctx), "native write": b.nativeWriteHeld(t, *b.source)} {
				if !held {
					t.Fatalf("calibration: %s refused before the change", name)
				}
			}
			before := b.generation(t, b.f.wsID)
			test.mutate(t, b.f)
			wantStored := test.unrelated
			for name, held := range map[string]bool{"receipt": b.receiptHeld(t), "run upload": b.runUploadHeld(t), "stream ticket": b.ticketHeld(t)} {
				if held != wantStored {
					t.Errorf("%s: held=%v after the change, want %v", name, held, wantStored)
				}
			}
			if moved := b.generation(t, b.f.wsID) != before; moved != (!test.unrelated && !test.noEpoch) {
				t.Errorf("user generation here moved=%v, unrelated=%v", moved, test.unrelated)
			}
			now := craftNativeFreshRequestContext(t, b.f, b.f.userID, "native-source")
			fresh := *auth.OriginalCredentialFromContext(now)
			staleUpload, freshUpload := b.humanUploadHeld(t, ctx), b.humanUploadHeld(t, now)
			staleWrite, freshWrite := b.nativeWriteHeld(t, *b.source), b.nativeWriteHeld(t, fresh)
			if staleUpload && !freshUpload {
				t.Errorf("upload: the old credential holds a right a new one does not")
			}
			if staleWrite && !freshWrite {
				t.Errorf("native write: the old credential holds a right a new one does not")
			}
			if test.noCreate && staleUpload {
				t.Errorf("upload: the old credential kept the right to create")
			}
			if test.noWrite && staleWrite {
				t.Errorf("native write: the old credential kept write")
			}
		})
	}
}

// The row dropped from TestCraftNativeSessionSurvivesHolderMembershipElsewhere:
// a restricted membership elsewhere is a ceiling, so a holder of one is never
// admitted and there is no session to keep. Removing that row widens authority.
// The old credential stays dead and only a new one is admitted.
func TestRestrictedRowElsewhereRefusesAdmissionAndItsRemovalRevivesNothing(t *testing.T) {
	f, m, _ := craftNativeHTTPFixture(t)
	mustExec(t, f.db, `INSERT INTO workspaces(id,name,slug) VALUES('boundary-other','Other','boundary-other')`)
	mustExec(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('boundary-other-member','boundary-other',?,'MEMBER','restricted')`, f.userID)
	stale := craftNativeFreshRequestContext(t, f, f.userID, "native-source")
	if w := craftNativeSessionCall(m, stale); w.Code != http.StatusForbidden {
		t.Fatalf("admission under a restricted row elsewhere: %d %s", w.Code, w.Body)
	}
	mustExec(t, f.db, `DELETE FROM workspace_members WHERE id='boundary-other-member'`)
	if w := craftNativeSessionCall(m, stale); w.Code != http.StatusForbidden {
		t.Fatalf("a credential from under the ceiling was admitted: %d %s", w.Code, w.Body)
	}
	fresh := craftNativeFreshRequestContext(t, f, f.userID, "native-source")
	w := craftNativeSessionCall(m, fresh)
	if w.Code != http.StatusCreated {
		t.Fatalf("new credential after the ceiling was lifted: %d %s", w.Code, w.Body)
	}
	var issued map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
}
