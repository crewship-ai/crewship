package server

// Coverage tests for routes_container.go: provider-absent and
// provider-error branches of status/start/stop, the slug-resolved stop
// path including the optional SidecarProvider stop, exec failures in
// the file-list and git-log introspection handlers, and the git-log
// agent_slug sanitization.

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// covErrContainer fails the operations the tests select via flags;
// everything else behaves like mockContainer.
type covErrContainer struct {
	mockContainer
	statusErr bool
	ensureErr bool
	execErr   bool
}

func (c *covErrContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	if c.statusErr {
		return nil, errors.New("daemon unreachable")
	}
	return &provider.ContainerStatus{ID: id, State: "running", Uptime: "1h"}, nil
}

func (c *covErrContainer) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	if c.ensureErr {
		return "", errors.New("image pull failed")
	}
	return "container-" + cfg.ID, nil
}

func (c *covErrContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	if c.execErr {
		return nil, errors.New("exec create failed")
	}
	return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(""))}, nil
}

// covSidecarContainer records the stop calls and implements the
// optional provider.SidecarProvider capability.
type covSidecarContainer struct {
	mockContainer
	stopErr        error
	sidecarErr     error
	stoppedRuntime string
	stoppedSidecar string
}

func (c *covSidecarContainer) StopCrewRuntime(_ context.Context, containerID string) error {
	c.stoppedRuntime = containerID
	return c.stopErr
}

func (c *covSidecarContainer) EnsureCrewServices(_ context.Context, _ provider.CrewConfig) (map[string]string, error) {
	return nil, nil
}
func (c *covSidecarContainer) StopCrewServices(_ context.Context, crewSlug string) error {
	c.stoppedSidecar = crewSlug
	return c.sidecarErr
}
func (c *covSidecarContainer) RemoveCrewServices(_ context.Context, _ string) error { return nil }

// covExecRecordingContainer captures the ExecConfig so tests can assert
// on the constructed command/workdir, and returns canned output.
type covExecRecordingContainer struct {
	mockContainer
	output   string
	lastExec provider.ExecConfig
}

func (c *covExecRecordingContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	c.lastExec = cfg
	return &provider.ExecResult{ExecID: "e", Reader: io.NopCloser(strings.NewReader(c.output))}, nil
}

func TestHandleContainerStatus_NoProvider(t *testing.T) {
	s := newTestServerForT(t)
	req := httptest.NewRequest("GET", "/crews/c1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "not_configured" {
		t.Errorf("status = %v, want not_configured", resp["status"])
	}
}

func TestHandleContainerStatus_ProviderErrorIsUnknown(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covErrContainer{statusErr: true}
	req := httptest.NewRequest("GET", "/crews/c1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "unknown" {
		t.Errorf("status = %v, want unknown on provider error", resp["status"])
	}
}

func TestHandleContainerStart_NoProvider503(t *testing.T) {
	s := newTestServerForT(t)
	req := httptest.NewRequest("POST", "/crews/c1/container/start", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandleContainerStart_EnsureError500(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covErrContainer{ensureErr: true}
	req := httptest.NewRequest("POST", "/crews/c1/container/start", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["error"] != "container start failed" {
		t.Errorf("error = %v, want container-start-failed", resp["error"])
	}
}

func TestHandleContainerStop_NoProvider503(t *testing.T) {
	s := newTestServerForT(t)
	req := httptest.NewRequest("POST", "/crews/c1/container/stop", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandleContainerStop_ResolvesSlugAndStopsSidecars(t *testing.T) {
	s := newTestServerWithDeps(t)
	ctr := &covSidecarContainer{}
	s.container = ctr
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_cs','CS','ws-cs')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_cs','ws_cs','CS','alpha-cs')`)

	req := httptest.NewRequest("POST", "/crews/crew_cs/container/stop", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// Container name must be derived from the crew slug via the provider.
	if ctr.stoppedRuntime != "crewship-team-alpha-cs" {
		t.Errorf("stopped runtime = %q, want crewship-team-alpha-cs", ctr.stoppedRuntime)
	}
	// Sidecar services stopped on the same slug.
	if ctr.stoppedSidecar != "alpha-cs" {
		t.Errorf("stopped sidecar slug = %q, want alpha-cs", ctr.stoppedSidecar)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "stopped" {
		t.Errorf("status = %v, want stopped", resp["status"])
	}
}

func TestHandleContainerStop_SidecarFailureIsBestEffort(t *testing.T) {
	s := newTestServerWithDeps(t)
	ctr := &covSidecarContainer{sidecarErr: errors.New("postgres refused to die")}
	s.container = ctr
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_sb','SB','ws-sb')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_sb','ws_sb','SB','beta-sb')`)

	req := httptest.NewRequest("POST", "/crews/crew_sb/container/stop", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	// Sidecar stop failures must not break the crew-stop response.
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 despite sidecar error", w.Code)
	}
}

func TestHandleContainerStop_RuntimeStopError500(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covSidecarContainer{stopErr: errors.New("cannot stop")}

	req := httptest.NewRequest("POST", "/crews/unknown-crew/container/stop", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["error"] != "container stop failed" {
		t.Errorf("error = %v, want container-stop-failed", resp["error"])
	}
}

func TestHandleContainerFileList_ExecErrorDegradesToEmpty(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covErrContainer{execErr: true}
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_fl','FL','ws-fl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_fl','ws_fl','FL','fl-slug')`)

	req := httptest.NewRequest("GET", "/crews/crew_fl/container-files", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	files, ok := resp["files"].([]interface{})
	if !ok || len(files) != 0 {
		t.Errorf("files = %v, want empty array on exec failure", resp["files"])
	}
}

func TestHandleContainerFileList_CrewNotFound404(t *testing.T) {
	s := newTestServerWithDeps(t)
	req := httptest.NewRequest("GET", "/crews/no-such-crew/container-files", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["error"] != "crew not found" {
		t.Errorf("error = %v, want crew-not-found", resp["error"])
	}
}

func TestHandleContainerGitLog_ExecErrorReportsGitUnavailable(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covErrContainer{execErr: true}
	mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_gl','GL','ws-gl')`)
	mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_gl','ws_gl','GL','gl-slug')`)

	req := httptest.NewRequest("GET", "/crews/crew_gl/git-log", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["error"] != "git not available" {
		t.Errorf("error = %v, want git-not-available", resp["error"])
	}
	commits, ok := resp["commits"].([]interface{})
	if !ok || len(commits) != 0 {
		t.Errorf("commits = %v, want empty array", resp["commits"])
	}
}

func TestHandleContainerGitLog_AgentSlugWorkDir(t *testing.T) {
	cases := []struct {
		name      string
		agentSlug string
		wantDir   string
	}{
		{"valid slug scopes to agent output", "bob-agent", "/output/bob-agent"},
		{"dot-dot stays home", "..", "/home"},
		{"path-y slug uses base name", "x/y-agent", "/output/y-agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServerWithDeps(t)
			ctr := &covExecRecordingContainer{output: "abc|fix: thing|Alice|2026-06-01T00:00:00Z\n"}
			s.container = ctr
			mustExec(t, s.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_wd','WD','ws-wd')`)
			mustExec(t, s.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew_wd','ws_wd','WD','wd-slug')`)

			req := httptest.NewRequest("GET", "/crews/crew_wd/git-log?agent_slug="+tc.agentSlug, nil)
			w := httptest.NewRecorder()
			s.ipcMux.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if ctr.lastExec.WorkingDir != tc.wantDir {
				t.Errorf("workdir = %q, want %q", ctr.lastExec.WorkingDir, tc.wantDir)
			}
			// The canned commit line must round-trip through the parser.
			resp := parseJSON(t, w.Body.Bytes())
			commits, ok := resp["commits"].([]interface{})
			if !ok || len(commits) != 1 {
				t.Fatalf("commits = %v, want 1 parsed commit", resp["commits"])
			}
			c := commits[0].(map[string]interface{})
			if c["hash"] != "abc" || c["author"] != "Alice" {
				t.Errorf("commit = %v, want hash=abc author=Alice", c)
			}
		})
	}
}
