package api

// agent_persona.go coverage top-up #2 — the storage-IO failure forks
// (write/load/reset persona), the policy-resolver failure fallback, the
// audit/inbox enqueue failure 500s, and the version-record warn paths.
//
// Storage failures are injected by planting a regular file where the
// handler expects the .memory directory (MkdirAll → ENOTDIR) or by
// stripping write permission from the directory. All tests are
// prefixed TestCov2AP.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

// cov2APBlockMemoryDir plants a regular file at the agent's .memory dir
// path so MkdirAll / reads fail with ENOTDIR.
func cov2APBlockMemoryDir(t *testing.T, r *personaTestRig) {
	t.Helper()
	agentDir := filepath.Join(r.output, "crews", r.crewID, "agents", "alice")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, ".memory"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("plant file: %v", err)
	}
}

func cov2APBlockCrewSharedDir(t *testing.T, r *personaTestRig) {
	t.Helper()
	sharedDir := filepath.Join(r.output, "crews", r.crewID, "shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, ".memory"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("plant file: %v", err)
	}
}

// --- agent: write / load / reset failures ---

func TestCov2APPutAgentPersona_WriteFailure500(t *testing.T) {
	r := newPersonaTestRig(t)
	cov2APBlockMemoryDir(t, r)
	rec := httptest.NewRecorder()
	r.h.PutAgentPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": "x"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (ENOTDIR write), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2APGetAgentPersona_LoadFailure500(t *testing.T) {
	r := newPersonaTestRig(t)
	cov2APBlockMemoryDir(t, r)
	rec := httptest.NewRecorder()
	r.h.GetAgentPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (ENOTDIR load), body=%s", rec.Code, rec.Body.String())
	}
}

func TestCov2APDeleteAgentPersona_ResetFailure500(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-bit test is meaningless as root")
	}
	r := newPersonaTestRig(t)
	// Land a persona first, then lock the directory so the delete fails.
	rec := httptest.NewRecorder()
	r.h.PutAgentPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": "keepme"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed put: %d %s", rec.Code, rec.Body.String())
	}
	memDir := filepath.Join(r.output, "crews", r.crewID, "agents", "alice", ".memory")
	if err := os.Chmod(memDir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(memDir, 0o755) })

	rec = httptest.NewRecorder()
	r.h.DeleteAgentPersona(rec, r.authedReq(t, http.MethodDelete, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (unlink denied), body=%s", rec.Code, rec.Body.String())
	}
}

// --- agent: missing workspace context → lookup error ---

func TestCov2APGetAgentPersona_NoWorkspaceContext(t *testing.T) {
	r := newPersonaTestRig(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("agentId", r.agentID)
	rec := httptest.NewRecorder()
	r.h.GetAgentPersona(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (workspace context missing), body=%s", rec.Code, rec.Body.String())
	}
}

// --- suggest: auto-apply write failure → 500 ---

func TestCov2APSuggest_AutoApplyWriteFailure500(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`UPDATE crews SET autonomy_level='full' WHERE id=?`, r.crewID); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}
	if _, err := r.h.db.Exec(`UPDATE agents SET self_learning_enabled = 1 WHERE id = ?`, r.agentID); err != nil {
		t.Fatalf("self_learning on: %v", err)
	}
	r.h.policyResolver.Invalidate(r.crewID)
	cov2APBlockMemoryDir(t, r)

	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{"content": "be bold"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (auto-apply write blocked), body=%s", rec.Code, rec.Body.String())
	}
}

// --- suggest: audit_logs enqueue failure → 500 ---

func TestCov2APSuggest_EnqueueFailure500(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`DROP TABLE audit_logs`); err != nil {
		t.Fatalf("drop audit_logs: %v", err)
	}
	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{"content": "queue me"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (enqueue proposal), body=%s", rec.Code, rec.Body.String())
	}
}

// --- suggest: gate-demoted inbox enqueue failure → 500 ---

func TestCov2APSuggest_GatedInboxInsertFailure500(t *testing.T) {
	r := newPersonaTestRig(t)
	// Full autonomy (auto-apply) + self_learning OFF → demoted to inbox.
	if _, err := r.h.db.Exec(`UPDATE crews SET autonomy_level='full' WHERE id=?`, r.crewID); err != nil {
		t.Fatalf("set autonomy: %v", err)
	}
	if _, err := r.h.db.Exec(`UPDATE agents SET self_learning_enabled = 0 WHERE id = ?`, r.agentID); err != nil {
		t.Fatalf("self_learning off: %v", err)
	}
	r.h.policyResolver.Invalidate(r.crewID)
	if _, err := r.h.db.Exec(`DROP TABLE inbox_items`); err != nil {
		t.Fatalf("drop inbox_items: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{"content": "gated"}))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (gated inbox insert failed), body=%s", rec.Code, rec.Body.String())
	}
}

// --- suggest: policy resolver failure falls back to inbox-approve ---

func TestCov2APSuggest_ResolverErrorDefaultsToInbox(t *testing.T) {
	r := newPersonaTestRig(t)
	// A resolver over a closed DB always errors → warn + inbox default.
	deadDB, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = deadDB.Close()
	h := NewPersonaHandler(r.h.db, newTestLogger(), r.output, policy.NewResolver(deadDB))

	rec := httptest.NewRecorder()
	h.SuggestAgentPersona(rec, r.authedReq(t, http.MethodPost, "/", map[string]string{"content": "resolver down"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (default inbox), body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["pending"] != true || got["applied"] != false {
		t.Errorf("got %+v, want pending=true applied=false (inbox default)", got)
	}
}

// --- recordVersion failures are warn-only (PUT still succeeds) ---

func TestCov2APPutPersona_VersionRecordFailureIsSoft(t *testing.T) {
	r := newPersonaTestRig(t)
	if _, err := r.h.db.Exec(`DROP TABLE memory_versions`); err != nil {
		t.Fatalf("drop memory_versions: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.PutAgentPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": "agent layer"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("agent put status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": "crew layer"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("crew put status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
}

// --- crew: write / load / reset failures ---

func TestCov2APCrewPersona_StorageFailures(t *testing.T) {
	t.Run("put ENOTDIR 500", func(t *testing.T) {
		r := newPersonaTestRig(t)
		cov2APBlockCrewSharedDir(t, r)
		rec := httptest.NewRecorder()
		r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": "x"}))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("get ENOTDIR 500", func(t *testing.T) {
		r := newPersonaTestRig(t)
		cov2APBlockCrewSharedDir(t, r)
		rec := httptest.NewRecorder()
		r.h.GetCrewPersona(rec, r.authedReq(t, http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("delete unlink denied 500", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("permission-bit test is meaningless as root")
		}
		r := newPersonaTestRig(t)
		rec := httptest.NewRecorder()
		r.h.PutCrewPersona(rec, r.authedReq(t, http.MethodPut, "/", map[string]string{"content": "keep"}))
		if rec.Code != http.StatusOK {
			t.Fatalf("seed put: %d %s", rec.Code, rec.Body.String())
		}
		memDir := filepath.Join(r.output, "crews", r.crewID, "shared", ".memory")
		if err := os.Chmod(memDir, 0o555); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(memDir, 0o755) })

		rec = httptest.NewRecorder()
		r.h.DeleteCrewPersona(rec, r.authedReq(t, http.MethodDelete, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500, body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("no workspace context", func(t *testing.T) {
		r := newPersonaTestRig(t)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("crewId", r.crewID)
		req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: "u1"}))
		rec := httptest.NewRecorder()
		r.h.GetCrewPersona(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("status = %d, want error without workspace context, body=%s", rec.Code, rec.Body.String())
		}
	})
}
