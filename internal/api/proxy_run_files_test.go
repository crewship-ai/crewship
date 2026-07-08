package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Tests for RunFiles — GET .../pipeline-runs/{runId}/files (#839).
//
// The endpoint resolves a run to its crew, lists the crew's files over
// IPC, and keeps only those whose mtime falls inside the run window.

// runFilesIPC stands up a crewshipd stub that serves /crews/{id}/files,
// returning a different file set for the /output tree vs the shared tree
// (subdir=shared), each with controlled mod_times.
func runFilesIPC(t *testing.T, outputFiles, sharedFiles []ipcFileInfo) string {
	t.Helper()
	return newUnixIPCServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		files := outputFiles
		if r.URL.Query().Get("subdir") == "shared" {
			files = sharedFiles
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"crew_id": "crew-rf", "files": files})
	}))
}

func fi(path, name string, mod time.Time, isDir bool) ipcFileInfo {
	return ipcFileInfo{Path: path, Name: name, Size: 10, IsDir: isDir, ModTime: mod}
}

func seedRunForFiles(t *testing.T, h *ProxyHandler, wsID, crewID, runID, startedAt, endedAt string) {
	t.Helper()
	seedRunsPipeline(t, h.db, wsID, "pl-rf", "rf") // satisfy pipeline_runs.pipeline_id FK
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.Exec(`
		INSERT INTO pipeline_runs (
		    id, workspace_id, pipeline_id, pipeline_slug, status, mode,
		    started_at, ended_at, invoking_crew_id,
		    step_outputs_json, cost_usd, duration_ms, triggered_via, inputs_json, created_at, updated_at
		) VALUES (?, ?, 'pl-rf', 'rf', 'completed', 'run', ?, ?, ?, '{}', 0, 0, 'manual', '{}', ?, ?)`,
		runID, wsID, startedAt, endedAt, crewID, now, now); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

func TestRunFiles_WindowFilters(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 1, 11, 0, 0, 0, time.UTC)

	output := []ipcFileInfo{
		fi("crew-rf/writer/report.pdf", "report.pdf", start.Add(30*time.Minute), false), // in window ✓
		fi("crew-rf/writer/old.txt", "old.txt", start.Add(-1*time.Hour), false),         // before ✗
		fi("crew-rf/writer/late.txt", "late.txt", end.Add(1*time.Hour), false),          // after ✗
		fi("crew-rf/writer", "writer", start.Add(30*time.Minute), true),                 // dir ✗
	}
	shared := []ipcFileInfo{
		fi("crews/crew-rf/shared/summary.md", "summary.md", start.Add(20*time.Minute), false), // in window ✓
		fi("crews/crew-rf/shared/stale.md", "stale.md", start.Add(-2*time.Hour), false),       // before ✗
	}

	sock := runFilesIPC(t, output, shared)
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	seedCrewRow(t, h.db, "crew-rf", wsID, "RF", "rf")
	seedRunForFiles(t, h, wsID, "crew-rf", "run-rf",
		start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))

	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/run-rf/files", nil)
	req.SetPathValue("workspaceId", wsID)
	req.SetPathValue("runId", "run-rf")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RunFiles(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp runFilesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.CrewID != "crew-rf" {
		t.Fatalf("crew_id = %q, want crew-rf", resp.CrewID)
	}
	got := map[string]bool{}
	for _, f := range resp.Files {
		got[f.Name] = true
	}
	if len(resp.Files) != 2 || !got["report.pdf"] || !got["summary.md"] {
		names := []string{}
		for _, f := range resp.Files {
			names = append(names, f.Name)
		}
		t.Fatalf("produced files = %v, want exactly [report.pdf summary.md]", names)
	}
}

func TestRunFiles_RunNotFound(t *testing.T) {
	sock := runFilesIPC(t, nil, nil)
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)

	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/nope/files", nil)
	req.SetPathValue("runId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RunFiles(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestRunFiles_EmptyRole_Forbidden(t *testing.T) {
	h := newProxyHandlerForTest(t, "/tmp/no-such-socket-rf")
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	req := httptest.NewRequest("GET", "/x/files", nil)
	req.SetPathValue("runId", "r")
	req = withWorkspaceUser(req, userID, wsID, "") // empty role → fail closed
	rr := httptest.NewRecorder()
	h.RunFiles(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// A run whose crew resolves to another workspace must not leak that crew's
// files — the tenant re-validation returns an empty list.
func TestRunFiles_CrossWorkspaceCrew_Empty(t *testing.T) {
	start := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	sock := runFilesIPC(t,
		[]ipcFileInfo{fi("other/x.pdf", "x.pdf", start.Add(10*time.Minute), false)}, nil)
	h := newProxyHandlerForTest(t, sock)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	// Crew "crew-foreign" is NOT inserted into any crews row for wsID, so
	// the tenant guard (SELECT 1 FROM crews ...) fails → empty.
	seedRunForFiles(t, h, wsID, "crew-foreign", "run-x",
		start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))

	req := httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-runs/run-x/files", nil)
	req.SetPathValue("runId", "run-x")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.RunFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp runFilesResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Files) != 0 {
		t.Fatalf("expected no files for cross-workspace crew, got %d", len(resp.Files))
	}
}
