package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// A full workspace refuses the publish with a distinct storage code, keeps
// every confirmed version readable, and warns through a coarse level only.
func TestArtifactProjectAggregateStorageRefusalKeepsHistoryAndUsesStorageCode(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	if created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	mustExec(t, h.db, `UPDATE artifact_storage_workspaces SET max_bytes=used_bytes WHERE workspace_id=?`, ws)
	got := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(1, "Version two"))
	var refusal struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(got.Body.Bytes(), &refusal)
	if got.Code != 413 || refusal.Code != "artifact_storage_quota" || !strings.Contains(refusal.Error, "retained") {
		t.Fatalf("aggregate refusal %d %s", got.Code, got.Body)
	}
	read := call(owner, "GET", "three-screens/artifact", "")
	if read.Code != 200 {
		t.Fatalf("read after refusal %d %s", read.Code, read.Body)
	}
	var snap struct {
		LatestVersion int64                  `json:"latest_version"`
		Quota         artifacts.VersionQuota `json:"quota"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.LatestVersion != 1 || snap.Quota.StorageLevel != artifacts.QuotaLevelFull || !snap.Quota.Warning {
		t.Fatalf("snapshot after refusal %+v", snap)
	}
	for _, private := range []string{"workspace_used_bytes", "reserved_bytes", "storage\""} {
		if strings.Contains(read.Body.String(), private) {
			t.Fatalf("Page snapshot disclosed aggregate usage (%s): %s", private, read.Body)
		}
	}
}

// Only workspace owners and admins read workspace-wide byte counts; no one
// reads instance byte counts through it.
func TestArtifactStorageUsageIsOwnerOrAdminOnly(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := strings.TrimPrefix(artifactProjectBody(0, "Version one"), "{")
	if created := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Three screens",`+body); created.Code != 201 {
		t.Fatalf("create %d %s", created.Code, created.Body)
	}
	member := "artifact-storage-member"
	mustExec(t, h.db, `INSERT INTO users(id,email,full_name) VALUES(?,'storage-member@example.com','Member')`, member)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, member)
	// The fixture already minted the owner's token (one row per user).
	tokens := map[string]string{owner: "crewship_cli_artifact-project-owner", member: mintTokenFor(t, h.db, member, "artifact-storage-member")}
	usage := func(user, ctxRole string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/pages/artifacts/storage", nil)
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, ctxRole)
		ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
		w := httptest.NewRecorder()
		h.ArtifactStorageUsage(w, req.WithContext(ctx))
		return w
	}
	// A stale OWNER claim in the request context must not override the live role.
	if denied := usage(member, "OWNER"); denied.Code != 403 || strings.Contains(denied.Body.String(), "used_bytes") {
		t.Fatalf("member read workspace usage %d %s", denied.Code, denied.Body)
	}
	got := usage(owner, "OWNER")
	if got.Code != 200 {
		t.Fatalf("owner usage %d %s", got.Code, got.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if used, _ := out["used_bytes"].(float64); used <= 0 || out["level"] != artifacts.QuotaLevelNone || out["retention"] != "keep-all" {
		t.Fatalf("owner usage body %s", got.Body)
	}
	for key := range out {
		switch key {
		case "used_bytes", "reserved_bytes", "limit_bytes", "level", "instance_level", "retention":
		default:
			t.Fatalf("usage disclosed extra field %q: %s", key, got.Body)
		}
	}
}
