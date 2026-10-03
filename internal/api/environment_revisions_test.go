package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func revisionFixture() *devcontainer.ProvisionResult {
	return &devcontainer.ProvisionResult{CachedImage: "crewship-cache:new", ConfigHash: "build-hash", Requirements: devcontainer.AggregatedRequirements{Toolchain: &devcontainer.ToolchainInventory{SchemaVersion: 1, Status: "recorded", ImageID: "sha256:" + strings.Repeat("a", 64), Tools: []devcontainer.ToolchainTool{{Binary: "codex", Version: "0.152.0", Status: "observed", Path: "/usr/local/bin/codex"}}}}}
}
func TestEnvironmentRevisionPersistsWithoutMutableDefinitionSecrets(t *testing.T) {
	config := `{"image":"ubuntu:22.04"}`
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, config)
	expected := provisionDefinition{Config: config}
	result := revisionFixture()
	id, err := h.saveProvisionResult(context.Background(), crewID, wsID, expected, result)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.saveProvisionResult(context.Background(), crewID, wsID, expected, result)
	if err != nil || again != id || id == "" {
		t.Fatalf("idempotent record %q %q %v", id, again, err)
	}
	if _, err := h.db.Exec(`UPDATE environment_revisions SET image_id='changed' WHERE id=?`, id); err == nil {
		t.Fatal("revision mutation allowed")
	}
	result.Requirements.Toolchain.ImageID = "sha256:" + strings.Repeat("b", 64)
	next, err := h.saveProvisionResult(context.Background(), crewID, wsID, expected, result)
	if err != nil || next == id {
		t.Fatalf("new artifact did not create revision: %v", err)
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM environment_revisions WHERE crew_id=?`, crewID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("count %d err %v", count, err)
	}
	for _, tc := range []struct {
		workspace, crew string
		want            int
	}{{wsID, crewID, 200}, {"foreign", crewID, 404}, {wsID, "missing", 404}} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("crewId", tc.crew)
		req = withWorkspaceUser(req, "test-user-id", tc.workspace, "OWNER")
		rr := httptest.NewRecorder()
		h.EnvironmentRevisions(rr, req)
		if rr.Code != tc.want {
			t.Fatalf("status %d want %d: %s", rr.Code, tc.want, rr.Body.String())
		}
		if rr.Code == 200 {
			var body struct {
				Revisions []EnvironmentRevision `json:"revisions"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || len(body.Revisions) != 2 {
				t.Fatalf("history %s %v", rr.Body.String(), err)
			}
		}
	}
}
func TestEnvironmentRevisionRejectsStaleBuildWithoutOverwritingCache(t *testing.T) {
	for _, change := range []string{"config", "mise", "runtime", "adapters", "deleted"} {
		t.Run(change, func(t *testing.T) {
			config := `{"image":"ubuntu:22.04"}`
			h, wsID, crewID := covProvRig(t, &covCommitClient{}, config)
			_, err := h.db.Exec(`UPDATE crews SET cached_image='current' WHERE id=?`, crewID)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "config":
				_, err = h.db.Exec(`UPDATE crews SET devcontainer_config='{"image":"ubuntu:24.04"}' WHERE id=?`, crewID)
			case "mise":
				_, err = h.db.Exec(`UPDATE crews SET mise_config='{"tools":{"node":"22"}}' WHERE id=?`, crewID)
			case "runtime":
				_, err = h.db.Exec(`UPDATE crews SET runtime_image='other' WHERE id=?`, crewID)
			case "adapters":
				_, err = h.db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,cli_adapter) VALUES(?,?,?,?,?,?)`, "revision-agent", wsID, crewID, "Revision agent", "revision-agent", "codex")
			case "deleted":
				_, err = h.db.Exec(`UPDATE crews SET deleted_at='2026-10-03T00:00:00Z' WHERE id=?`, crewID)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = h.saveProvisionResult(context.Background(), crewID, wsID, provisionDefinition{Config: config}, revisionFixture())
			if err == nil || (change != "deleted" && !errors.Is(err, errBuildDefinitionChanged)) {
				t.Fatalf("stale result accepted: %v", err)
			}
			var image string
			var count int
			if err := h.db.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, crewID).Scan(&image); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM environment_revisions WHERE crew_id=?`, crewID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if image != "current" || count != 0 {
				t.Fatalf("stale mutation image=%q count=%d", image, count)
			}
		})
	}
}
func TestEnvironmentRevisionsRequiresReadRole(t *testing.T) {
	h := &ProvisioningHandler{}
	rr := httptest.NewRecorder()
	h.EnvironmentRevisions(rr, httptest.NewRequest("GET", "/x", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatal(rr.Code)
	}
}
