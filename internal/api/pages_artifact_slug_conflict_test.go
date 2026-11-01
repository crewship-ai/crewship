package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactProjectCreateOccupiedSlugHasStableErrorAndPreservesProject(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	body := `{"slug":"three-screens","name":"Original project",` + strings.TrimPrefix(artifactProjectBody(0, "Original content"), "{")
	created := call(owner, "POST", "artifacts", body)
	if created.Code != 201 {
		t.Fatalf("create calibration: %d %s", created.Code, created.Body)
	}
	before := call(owner, "GET", "three-screens/artifact", "")
	if before.Code != 200 {
		t.Fatalf("read calibration: %d %s", before.Code, before.Body)
	}
	collision := call(owner, "POST", "artifacts", strings.Replace(body, "Original content", "Unwanted replacement", 1))
	var problem struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if json.Unmarshal(collision.Body.Bytes(), &problem) != nil || collision.Code != 409 || problem.Code != "artifact_slug_taken" || problem.Error != "This address is already in use. Choose another slug." {
		t.Fatalf("wrong create error: %d %s", collision.Code, collision.Body)
	}
	cas := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(99, "Unwanted CAS edit"))
	var conflict map[string]any
	if json.Unmarshal(cas.Body.Bytes(), &conflict) != nil || cas.Code != 409 || conflict["code"] != "artifact_version_conflict" {
		t.Fatalf("publish lost its CAS classification: %d %s", cas.Code, cas.Body)
	}
	after := call(owner, "GET", "three-screens/artifact", "")
	if after.Code != 200 || !sameArtifactRead(t, before.Body.Bytes(), after.Body.Bytes()) {
		t.Fatalf("collision changed project: %d %s", after.Code, after.Body)
	}
	var count int
	if err := h.db.QueryRow(`SELECT count(*) FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&count); err != nil || count != 1 {
		t.Fatalf("collision left an extra Page: %d %v", count, err)
	}
	if err := h.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE workspace_id=?`, ws).Scan(&count); err != nil || count != 1 {
		t.Fatalf("collision left version: %d %v", count, err)
	}
}

// sameArtifactRead compares two artifact reads, ignoring the retention preview's
// evaluation time: it has second resolution, so two reads can straddle a second.
func sameArtifactRead(t *testing.T, before, after []byte) bool {
	t.Helper()
	normalize := func(raw []byte) string {
		var read map[string]any
		if err := json.Unmarshal(raw, &read); err != nil {
			t.Fatalf("artifact read is not JSON: %v %s", err, raw)
		}
		if preview, ok := read["retention_preview"].(map[string]any); ok {
			delete(preview, "evaluated_at")
		}
		out, _ := json.Marshal(read)
		return string(out)
	}
	return normalize(before) == normalize(after)
}
