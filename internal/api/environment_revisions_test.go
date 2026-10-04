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

func TestEnvironmentRevisionChecksSupersessionBeforeOldQualification(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "superseded"}[changed], func(t *testing.T) {
			config := `{"image":"ubuntu:22.04"}`
			h, ws, crew := covProvRig(t, &covCommitClient{}, config)
			oldMise := `{"ai_cli_check":"required"}`
			currentMise := oldMise
			if changed {
				currentMise = `{"ai_cli_check":"record"}`
			}
			if _, err := h.db.Exec(`UPDATE crews SET mise_config=?,cached_image='previous' WHERE id=?`, currentMise, crew); err != nil {
				t.Fatal(err)
			}
			_, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config, Mise: oldMise}, revisionFixture())
			if changed && !errors.Is(err, errBuildDefinitionChanged) {
				t.Fatalf("obsolete qualification terminalized a superseded build: %v", err)
			}
			if !changed && (err == nil || errors.Is(err, errBuildDefinitionChanged)) {
				t.Fatalf("current definition did not enforce required qualification: %v", err)
			}
			var selected string
			if err := h.db.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, crew).Scan(&selected); err != nil || selected != "previous" {
				t.Fatalf("failed publication changed selection: %q %v", selected, err)
			}
		})
	}
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

func TestEnvironmentRevisionWithoutCLIInventoryKeepsArtifactIdentity(t *testing.T) {
	config := `{"image":"alpine:3"}`
	h, workspace, crew := covProvRig(t, &covCommitClient{}, config)
	image := "sha256:" + strings.Repeat("a", 64)
	id, err := h.saveProvisionResult(context.Background(), crew, workspace, provisionDefinition{Config: config}, &devcontainer.ProvisionResult{CachedImage: image, ConfigHash: "build"})
	if err != nil || id == "" {
		t.Fatalf("artifact revision missing: %q %v", id, err)
	}
	var got, inventory string
	if err := h.db.QueryRow(`SELECT image_id,toolchain_json FROM environment_revisions WHERE id=?`, id).Scan(&got, &inventory); err != nil {
		t.Fatal(err)
	}
	if got != image || inventory != "null" {
		t.Fatalf("invented CLI evidence: image=%q inventory=%q", got, inventory)
	}
}

func TestEnvironmentPublicationClearsPreviousArtifactEvidence(t *testing.T) {
	config := `{"image":"alpine:3"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	old := revisionFixture()
	old.Requirements.Privileged = true
	old.Features = []devcontainer.FeatureRecord{{Ref: "previous-feature", ID: "previous"}}
	if _, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, old); err != nil {
		t.Fatal(err)
	}
	// A successful no-customization result is known empty, not permission to
	// attach a previous image's privileges, versions or feature provenance.
	if _, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, &devcontainer.ProvisionResult{ConfigHash: "new-build"}); err != nil {
		t.Fatal(err)
	}
	var requirements, features string
	if err := h.db.QueryRow(`SELECT COALESCE(cached_requirements,''),COALESCE(resolved_features,'') FROM crews WHERE id=?`, crew).Scan(&requirements, &features); err != nil {
		t.Fatal(err)
	}
	if requirements != "" || features != "" {
		t.Fatalf("previous artifact evidence survived: requirements=%s features=%s", requirements, features)
	}
}

func TestEnvironmentRevisionKeepsQualificationBoundToArtifact(t *testing.T) {
	config := `{"image":"alpine:3"}`
	h, ws, crew := covProvRig(t, &covCommitClient{}, config)
	result := revisionFixture()
	result.Requirements.Toolchain.Qualification = &devcontainer.ToolchainQualification{Status: "passed", ImageID: result.Requirements.Toolchain.ImageID, Tools: []devcontainer.ToolchainProbe{{Binary: "codex", Status: "passed"}}}
	id, err := h.saveProvisionResult(t.Context(), crew, ws, provisionDefinition{Config: config}, result)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := h.db.QueryRow(`SELECT toolchain_json FROM environment_revisions WHERE id=?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evidence devcontainer.ToolchainInventory
	if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Qualification == nil || evidence.Qualification.Status != "passed" || evidence.Qualification.ImageID != evidence.ImageID {
		t.Fatalf("qualification not bound in durable record: %s", raw)
	}
}
