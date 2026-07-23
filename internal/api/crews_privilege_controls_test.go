package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// crews_privilege_controls_test.go covers server-side enforcement of the
// structured container-privilege controls (#1380). The Security-tab UI writes
// top-level devcontainer.json keys — privileged / capAdd / mounts — that the
// save path MUST validate rather than store-and-discard:
//
//   - privileged:true is refused unless the workspace opted in via
//     allow_privileged_credentials (403).
//   - capAdd entries outside the feature-path allowlist (NET_BIND_SERVICE) are
//     rejected (400).
//   - mount sources outside internal/devcontainer/mount_validate.go's allowlist
//     are rejected (400).
//
// These are the red-first tests: on the pre-fix head the save path validated
// only size + JSON syntax, so every privileged/cap/mount body was accepted (201).

func enablePrivilegedWorkspace(t *testing.T, h *CrewHandler, wsID string) {
	t.Helper()
	if _, err := h.db.Exec(
		`UPDATE workspaces SET allow_privileged_credentials = 1 WHERE id = ?`, wsID); err != nil {
		t.Fatalf("enable allow_privileged_credentials: %v", err)
	}
}

func TestCreateCrew_PrivilegedWithoutFlag_Forbidden(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Priv Crew", "slug": "priv-noflag",
		"devcontainer_config": `{"image":"debian:bookworm-slim","privileged":true}`,
	})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateCrew_PrivilegedWithFlag_Allowed(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	enablePrivilegedWorkspace(t, h, wsID)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Priv Crew", "slug": "priv-flag",
		"devcontainer_config": `{"image":"debian:bookworm-slim","privileged":true}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateCrew_DisallowedCap_Rejected(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Cap Crew", "slug": "cap-bad",
		"devcontainer_config": `{"image":"debian:bookworm-slim","capAdd":["SYS_ADMIN"]}`,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(strings.ToLower(rr.Body.String()), "capab") {
		t.Errorf("body = %s, want capability error", rr.Body.String())
	}
}

func TestCreateCrew_AllowedCap_Accepted(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Cap Crew", "slug": "cap-ok",
		"devcontainer_config": `{"image":"debian:bookworm-slim","capAdd":["NET_BIND_SERVICE"]}`,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCreateCrew_DisallowedMount_Rejected(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	rr := covCCPost(h, userID, wsID, map[string]any{
		"name": "Mount Crew", "slug": "mount-bad",
		"devcontainer_config": `{"image":"debian:bookworm-slim","mounts":[{"source":"/var/run/docker.sock","target":"/var/run/docker.sock","type":"bind"}]}`,
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(strings.ToLower(rr.Body.String()), "mount") {
		t.Errorf("body = %s, want mount error", rr.Body.String())
	}
}

func TestUpdateCrew_PrivilegedWithoutFlag_Forbidden(t *testing.T) {
	h, userID, wsID := covCCHandler(t)
	// Seed a plain crew first.
	rr := covCCPost(h, userID, wsID, map[string]any{"name": "Upd Crew", "slug": "upd-priv"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed create status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/api/v1/crews/"+created.ID,
		bytes.NewBufferString(`{"devcontainer_config":"{\"image\":\"debian:bookworm-slim\",\"privileged\":true}"}`))
	req.SetPathValue("crewId", created.ID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	pr := httptest.NewRecorder()
	h.Update(pr, req)
	if pr.Code != http.StatusForbidden {
		t.Fatalf("update status = %d, want 403; body=%s", pr.Code, pr.Body.String())
	}
}
