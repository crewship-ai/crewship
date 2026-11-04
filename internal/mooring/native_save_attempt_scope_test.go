package mooring

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Another document's newer head must never make this document's still-current
// base obsolete. Use the same actor, session and attempt nonce on both scopes;
// only the page/workspace binding protects the delayed original publication.
func TestNativeRetirementPruningPreservesAnotherPagesPendingAttempt(t *testing.T) {
	for _, otherWorkspace := range []bool{false, true} {
		name := "same workspace"
		if otherWorkspace {
			name = "other workspace"
		}
		t.Run(name, func(t *testing.T) {
			s, _, db, _, controlScope, principal := nativeStoreFixture(t)
			victimScope := EditLeaseScope{WorkspaceID: controlScope.WorkspaceID, ArtifactID: "other-page"}
			if otherWorkspace {
				victimScope.WorkspaceID = "other-workspace"
				if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('other-workspace','Other','other-workspace')`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'other-page','Other','u','{}')`, victimScope.ArtifactID, victimScope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			initial := []byte(`{"v":1}`)
			control, _, err := s.Initialize(t.Context(), controlScope, principal, initial)
			if err != nil {
				t.Fatal(err)
			}
			victim, _, err := s.Initialize(t.Context(), victimScope, principal, initial)
			if err != nil {
				t.Fatal(err)
			}

			lost := []byte(`{"v":"delayed victim"}`)
			entered, release := pauseValidation(s, lost)
			done := make(chan error, 1)
			go func() {
				_, err := s.SaveAttempt(context.Background(), victim, 1, "r-victim", nativeReceiptHash(lost), lost, "shared-attempt-id")
				done <- err
			}()
			released, finished := false, false
			unblock := func() {
				if !released {
					close(release)
					released = true
				}
			}
			defer func() {
				unblock()
				if !finished {
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("pending validation did not stop during cleanup")
					}
				}
			}()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("original save did not enter validation")
			}
			if _, err := s.ReconcileAttempt(t.Context(), victim, 1, "r-victim", nativeReceiptHash(lost), "shared-attempt-id"); !errors.Is(err, ErrNativeAttemptAbsent) {
				t.Fatalf("retire pending victim: %v", err)
			}
			fresh := []byte(`{"v":"new control head"}`)
			if _, err := s.SaveAttempt(t.Context(), control, 1, "r-control", nativeReceiptHash(fresh), fresh, "control-fresh-id"); err != nil {
				t.Fatal(err)
			}
			// This retirement runs global garbage collection. The control head
			// is newer than the victim's base, but the victim head is not.
			if _, err := s.ReconcileAttempt(t.Context(), control, 2, "r-control-next", nativeReceiptHash(fresh), "shared-attempt-id"); !errors.Is(err, ErrNativeAttemptAbsent) {
				t.Fatalf("control retirement: %v", err)
			}
			var rows int
			if err := db.QueryRow(`SELECT count(*) FROM craft_native_retired_attempts WHERE workspace_id=? AND page_id=? AND session_id=? AND attempt_id='shared-attempt-id' AND base_version=1 AND generation=?`, victimScope.WorkspaceID, victimScope.ArtifactID, victim.Holder.SessionID, victim.Generation).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("another page's head pruned the current-base victim fence: rows=%d err=%v", rows, err)
			}
			unblock()
			select {
			case err = <-done:
				finished = true
			case <-time.After(10 * time.Second):
				t.Fatal("original save did not finish")
			}
			if !errors.Is(err, ErrNativeAttemptRetired) {
				t.Fatalf("delayed original published after cross-page pruning: %v", err)
			}
			if state, err := s.Read(t.Context(), victimScope); err != nil || state.LatestVersion != 1 || string(state.Manifest.Files["document.json"].Content) != string(initial) {
				t.Fatalf("victim changed: head=%d err=%v", state.LatestVersion, err)
			}
		})
	}
}
