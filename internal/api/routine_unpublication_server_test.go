//go:build linux

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestRoutineUnpublicationWithdrawsAndRepublishClears(t *testing.T) {
	f := newServerPublicationHTTPFixture(t, true)
	unpublish := "/api/v1/workspaces/" + f.workspace + "/restricted-routine-library/publish-routine/unpublish"
	// A trusted legacy HEAD has no Guest publication to withdraw.
	f.call(f.publisherToken, "POST", unpublish, nil, 409)
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	execOrFatal(t, f.db, `INSERT INTO users(id,email) VALUES('run-only','run-only@example.test')`)
	execOrFatal(t, f.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('run-only-member',?,'run-only','VIEWER','restricted')`, f.workspace)
	f.call(mintTokenFor(t, f.db, "run-only", "run-only-session"), "POST", unpublish, nil, 404)
	if body := f.call(f.publisherToken, "POST", unpublish, map[string]any{"x": 1}, 400).Body.String(); !strings.Contains(body, "no request body") {
		t.Fatalf("unpublish accepted a body: %s", body)
	}
	response := f.call(f.publisherToken, "POST", unpublish, nil, 200)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unpublish response may be cached")
	}
	var withdrawn routineUnpublicationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &withdrawn); err != nil || withdrawn.ID != "publish-routine" || withdrawn.Version != 2 || withdrawn.WithdrawnAt == "" {
		t.Fatalf("unpublish response %+v %v", withdrawn, err)
	}
	var stored string
	var manifests, audits int
	if err := f.db.QueryRow(`SELECT publication_withdrawn_at FROM pipelines WHERE id='publish-routine'`).Scan(&stored); err != nil || stored != withdrawn.WithdrawnAt {
		t.Fatalf("withdrawal not stored: %q %v", stored, err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id='publish-routine' AND publication_authority_json IS NOT NULL`).Scan(&manifests); err != nil || manifests != 1 {
		t.Fatalf("withdrawal changed the immutable archive: %d %v", manifests, err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id=? AND json_extract(payload,'$.action')='unpublish' AND json_extract(payload,'$.pipeline_id')='publish-routine' AND actor_id='publish-user'`, f.workspace).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("missing atomic unpublish audit: %d %v", audits, err)
	}
	if chain, err := journal.VerifyChain(t.Context(), f.db, f.workspace); err != nil || !chain.OK {
		t.Fatal("audit chain invalid", err)
	}
	f.call(f.publisherToken, "POST", unpublish, nil, 409)

	// Republishing a reviewed draft makes the publication current again.
	var fresh guestRoutineDraft
	if err := json.Unmarshal(f.call(f.publisherToken, "GET", f.path, nil, 200).Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	f.call(f.publisherToken, "PUT", f.path, fresh.guestRoutineDraftContent, 200)
	if err := json.Unmarshal(f.call(f.publisherToken, "GET", f.path, nil, 200).Body.Bytes(), &fresh); err != nil {
		t.Fatal(err)
	}
	f.draft = fresh
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	var cleared bool
	if err := f.db.QueryRow(`SELECT publication_withdrawn_at IS NULL FROM pipelines WHERE id='publish-routine'`).Scan(&cleared); err != nil || !cleared {
		t.Fatalf("republish kept the withdrawal: %v", err)
	}
	// A trusted workspace OWNER may also withdraw it.
	f.call(f.ownerToken, "POST", unpublish, nil, 200)
	f.checkForeignKeys()
}

func TestWorkspaceGuestRoutineRunLimitSetting(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	path := "/api/v1/workspaces/" + f.workspace
	for _, invalid := range []int{0, 241} {
		f.call(f.ownerToken, "PATCH", path, map[string]any{"guest_routine_max_run_minutes": invalid}, 400)
	}
	var updated workspaceResponse
	if err := json.Unmarshal(f.call(f.ownerToken, "PATCH", path, map[string]any{"guest_routine_max_run_minutes": 45}, 200).Body.Bytes(), &updated); err != nil || updated.GuestRoutineMaxRunMinutes != 45 {
		t.Fatalf("limit not returned: %+v %v", updated, err)
	}
	var stored int
	if err := f.db.QueryRow(`SELECT guest_routine_max_run_minutes FROM workspaces WHERE id=?`, f.workspace).Scan(&stored); err != nil || stored != 45 {
		t.Fatalf("limit not stored: %d %v", stored, err)
	}
	// A Guest cannot change workspace policy.
	if code := f.call(f.publisherToken, "PATCH", path, map[string]any{"guest_routine_max_run_minutes": 240}, 0).Code; code < 400 {
		t.Fatalf("Guest changed the run limit: HTTP%d", code)
	}
}
