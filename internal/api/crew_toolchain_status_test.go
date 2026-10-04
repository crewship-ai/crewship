package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestProvisionStatusToolchainEvidenceRequiresBuiltImage(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	requirements, err := json.Marshal(devcontainer.AggregatedRequirements{Toolchain: &devcontainer.ToolchainInventory{SchemaVersion: 1, Status: "recorded", ImageID: "sha256:fixture", Tools: []devcontainer.ToolchainTool{{Binary: "claude", Version: "2.1.263", Status: "observed"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"crewship-cache:fixture", ""} {
		if _, err := h.db.Exec(`UPDATE crews SET cached_image=?, cached_requirements=? WHERE id=?`, image, string(requirements), crewID); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("crewId", crewID)
		req = withWorkspaceUser(req, "test-user-id", wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ProvisionStatus(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		var response struct {
			Toolchain struct {
				Built *devcontainer.ToolchainInventory `json:"built"`
			} `json:"toolchain"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if image == "" {
			if response.Toolchain.Built != nil {
				t.Fatal("old requirements were reported as a newly built image")
			}
		} else if response.Toolchain.Built == nil || response.Toolchain.Built.Tools[0].Version != "2.1.263" {
			t.Fatalf("inventory missing: %s", rr.Body.String())
		}
	}
}
