package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func artifactLeaseCall(t *testing.T, h *PageHandler, workspace, user, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := pagesGrantRequest(t, "GET", "/api/v1/pages/report/artifact/lease"+query, workspace, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	w := httptest.NewRecorder()
	h.GetArtifactLease(w, r)
	return w
}
func TestArtifactLeaseMetadataOnlyExactRevisionAndExpiry(t *testing.T) {
	h, _, clock, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>private source never in lease</h1>", "lease-first", 0), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "second source", "lease-second", 1), 201)
	// Metadata does not depend on blob configuration, nor on opening source bytes.
	h.artifactStoragePath = ""
	w := artifactLeaseCall(t, h, ws, user, "?revision=1")
	artifactExpect(t, w, 200)
	var result artifactLeaseMetadata
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, result.ExpiresAt)
	if err != nil || !expires.Equal(clock.Now().Add(15*time.Second)) {
		t.Fatalf("invalid expiry %s err=%v", result.ExpiresAt, err)
	}
	if result.PageID != pageID || result.Revision != 1 || result.RuntimeProfile != artifactStaticProfile || result.ContentSHA256 != attachmentDigest([]byte("<h1>private source never in lease</h1>")) {
		t.Fatalf("unexpected metadata %+v", result)
	}
	if strings.Contains(w.Body.String(), "private source") || w.Header().Get("Cache-Control") == "" {
		t.Fatalf("unsafe response %s headers=%v", w.Body.String(), w.Header())
	}
	for _, query := range []string{"", "?revision=0", "?revision=-1", "?revision=1&revision=2", "?revision=bad"} {
		artifactExpect(t, artifactLeaseCall(t, h, ws, user, query), 400)
	}
	artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision=3"), 404)
	execOrFatal(t, h.db, `DELETE FROM page_artifact_revisions WHERE page_id=? AND revision=1`, pageID)
	artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision=1"), 404)
}
func TestArtifactLeaseLiveGrantMembershipAndAccount(t *testing.T) {
	h, _, _, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "private", "lease", 0), 201)
	pagesSeedUser(t, h, ws, "lease-reader", "lease-reader@example.test", "MEMBER")
	// Forged/stale OWNER in the request must not grant administrator visibility.
	artifactExpect(t, artifactLeaseCall(t, h, ws, "lease-reader", "?revision=1"), 404)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','lease-reader','read',?)`, pageID, user)
	artifactExpect(t, artifactLeaseCall(t, h, ws, "lease-reader", "?revision=1"), 200)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id='lease-reader'`, pageID)
	artifactExpect(t, artifactLeaseCall(t, h, ws, "lease-reader", "?revision=1"), 404)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','lease-reader','read',?)`, pageID, user)
	execOrFatal(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id='lease-reader'`, ws)
	artifactExpect(t, artifactLeaseCall(t, h, ws, "lease-reader", "?revision=1"), 403)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('lease-foreign','Foreign','lease-foreign')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('lease-foreign-member','lease-foreign',?,'OWNER')`, user)
	artifactExpect(t, artifactLeaseCall(t, h, "lease-foreign", user, "?revision=1"), 404)
	execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-13T00:00:00Z' WHERE id=?`, user)
	artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision=1"), 403)
	artifactExpect(t, artifactLeaseCall(t, h, "foreign-workspace", user, "?revision=1"), 403)
}
func TestArtifactLeaseRejectsRunAuthorityAndDeletedWorkspace(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "private", "lease", 0), 201)
	r := pagesGrantRequest(t, "GET", "/?revision=1", ws, user, "OWNER", "")
	r.SetPathValue("slug", "report")
	r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, &artifactRunAuthority{HumanUserID: user, WorkspaceID: ws, AgentID: "agent", RunID: "wrong-run"}))
	w := httptest.NewRecorder()
	h.GetArtifactLease(w, r)
	artifactExpect(t, w, 401)
	execOrFatal(t, h.db, `UPDATE workspaces SET deleted_at='2026-10-13T00:00:00Z' WHERE id=?`, ws)
	artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision=1"), 403)
}
func TestArtifactLeaseLegacyProfileDoesNotActivateRuntime(t *testing.T) {
	h, _, _, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "legacy", "lease", 0), 201)
	// Insert a legitimate historical revision with absent canonical manifest.
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) SELECT 'legacy-lease',page_id,2,sha256,author_user_id,'legacy-lease','legacy-digest',created_at FROM page_artifact_revisions WHERE page_id=? AND revision=1`, pageID)
	w := artifactLeaseCall(t, h, ws, user, "?revision=2")
	artifactExpect(t, w, 200)
	var result artifactLeaseMetadata
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.RuntimeProfile != artifactStaticProfile {
		t.Fatalf("legacy profile=%q", result.RuntimeProfile)
	}
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,manifest_json) SELECT 'unsupported-lease',page_id,3,sha256,author_user_id,'unsupported-lease','unsupported-digest',created_at,'{"format_version":1,"runtime_profile":"interactive-html-v1"}' FROM page_artifact_revisions WHERE page_id=? AND revision=1`, pageID)
	artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision=3"), 409)
}

func TestArtifactLeaseRejectsCorruptCanonicalMetadata(t *testing.T) {
	h, _, _, ws, user, pageID := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "safe", "lease", 0), 201)
	var raw string
	if err := h.db.QueryRow(`SELECT manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=1`, pageID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for index, modify := range []func(map[string]any){
		func(m map[string]any) { m["unknown"] = true },
		func(m map[string]any) { m["entrypoint"] = "other.html" },
		func(m map[string]any) { m["source"] = map[string]any{"private": "unexpected"} },
		func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64) },
		func(m map[string]any) {
			m["files"].([]any)[0].(map[string]any)["size_bytes"] = maxArtifactHTMLBytes + 1
		},
		func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["mime"] = "text/javascript" },
	} {
		var manifest map[string]any
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
			t.Fatal(err)
		}
		modify(manifest)
		encoded, _ := json.Marshal(manifest)
		id := "corrupt-lease-" + strconv.Itoa(index)
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,manifest_json) SELECT ?,page_id,?,sha256,author_user_id,?,'corrupt-digest',created_at,? FROM page_artifact_revisions WHERE page_id=? AND revision=1`, id, index+2, id, string(encoded), pageID)
		artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision="+strconv.Itoa(index+2)), 409)
	}
}

func TestArtifactLeaseGloballyRestrictedMembershipCannotBypassHostCeiling(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "private", "lease", 0), 201)
	execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('lease-restricted-ws','Restricted','lease-restricted-ws')`)
	execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('lease-restricted-member','lease-restricted-ws',?,'MEMBER','restricted')`, user)
	artifactExpect(t, artifactLeaseCall(t, h, ws, user, "?revision=1"), 403)
}
