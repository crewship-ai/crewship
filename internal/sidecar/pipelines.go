package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sidecar pipeline handlers wrap main-API endpoints with author
// identity injected from the IPC config. The agent inside the
// container only knows about /pipelines/* on localhost:9119; the
// sidecar forwards to /api/v1/workspaces/{ws}/pipelines/* (or the
// internal save route) using the X-Internal-Token chain.
//
// Trust model: the agent cannot lie about which crew it belongs to.
// Crew + agent IDs come from s.ipc.{CrewID, AgentID}, never from the
// request body. This is the cross-crew reuse security gate — Crew B's
// agent calling /pipelines/save can never claim author_crew_id =
// crew_a, because the sidecar overwrites the field before forwarding.

// pipelinesSaveRequest mirrors the agent-facing body for /pipelines/save.
// Client supplies the DSL + a sample input set used for the test_run.
// Author identity is INJECTED by the sidecar from IPC config; any
// caller-supplied author_* fields are silently overwritten.
type pipelinesSaveRequest struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Definition   json.RawMessage `json:"definition"`
	SampleInputs map[string]any  `json:"sample_inputs"`
}

// pipelinesRunRequest is the agent-facing body for /pipelines/{slug}/run.
type pipelinesRunRequest struct {
	Inputs map[string]any `json:"inputs"`
	DryRun bool           `json:"dry_run,omitempty"`
}

// handlePipelinesSave runs the test_run gate inline against the
// supplied DSL, then on success forwards to the main API's internal
// save endpoint with author identity injected from IPC.
//
// The two-step (test_run → save) flow runs entirely inside the
// crewshipd binary even though the agent only sees one HTTP call;
// the sidecar fans the request out so the agent's authoring loop
// is single-call, save-failed-on-bad-DSL is a single round-trip.
//
// POST /pipelines/save
//
// Body: { name, description, definition, sample_inputs? }
func (s *Server) handlePipelinesSave(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	var body pipelinesSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Name == "" || len(body.Definition) == 0 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "name and definition required"})
		return
	}

	// Slug is derived from the name to keep the agent-side API
	// minimal — agents emit a name, the platform decides the slug.
	// Same shape we use for skills.
	slug := slugifyForPipelines(body.Name)
	if slug == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "name does not produce a valid slug"})
		return
	}

	// Step 1: forward to test_run on the public endpoint. The public
	// endpoint runs the DSL against the workspace's execution tier
	// using the wired AgentRunner. Passing test_run is mandatory for
	// step 2 to succeed (the store enforces the gate).
	testRunBody, err := json.Marshal(map[string]any{
		"definition":     body.Definition,
		"author_crew_id": s.ipc.CrewID,
		"sample_inputs":  body.SampleInputs,
	})
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "marshal test_run body"})
		return
	}
	testRunPath := "/api/v1/workspaces/" + s.ipc.WorkspaceID + "/pipelines/test_run"
	testRes, err := s.ipcRequestJSON(r.Context(), http.MethodPost, testRunPath, testRunBody)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "test_run forward: " + err.Error()})
		return
	}
	if testRes.status >= 400 {
		// Forward the test_run failure straight back so the agent
		// gets the parsing/validation error in its own retry loop.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(testRes.status)
		_, _ = w.Write(testRes.body)
		return
	}
	// Decode test_run result to confirm passed=true and capture the
	// timestamp the store will check at save time.
	var testRunResult struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(testRes.body, &testRunResult)
	if testRunResult.Status != "COMPLETED" {
		writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]any{
			"error":    "test_run did not complete cleanly; pipeline not saved",
			"test_run": json.RawMessage(testRes.body),
			"hint":     "fix the DSL or sample_inputs and retry",
			"status":   testRunResult.Status,
		})
		return
	}

	// Step 2: forward to internal save with author injected from IPC.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	saveBody, err := json.Marshal(map[string]any{
		"workspace_id":         s.ipc.WorkspaceID,
		"slug":                 slug,
		"name":                 body.Name,
		"description":          body.Description,
		"definition":           body.Definition,
		"author_crew_id":       s.ipc.CrewID,
		"author_agent_id":      s.ipc.AgentID,
		"author_chat_id":       s.ipc.ChatID,
		"last_test_run_at":     now,
		"last_test_run_passed": true,
	})
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "marshal save body"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPost, "/api/v1/internal/pipelines/save", "pipeline-save", saveBody)
}

// handlePipelinesList returns workspace-visible pipelines for the
// agent's workspace. Forwarded straight to the public list endpoint
// since the result is the same surface a user sees in the UI.
func (s *Server) handlePipelinesList(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	path := "/api/v1/workspaces/" + s.ipc.WorkspaceID + "/pipelines"
	if q := r.URL.RawQuery; q != "" {
		path += "?" + q
	}
	s.proxyIPCJSON(w, r, http.MethodGet, path, "pipeline-list", nil)
}

// handlePipelinesGet returns one pipeline by slug. URL shape:
//
//	/pipelines/{slug}
func (s *Server) handlePipelinesGet(w http.ResponseWriter, r *http.Request, slug string) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	path := "/api/v1/workspaces/" + s.ipc.WorkspaceID + "/pipelines/" + slug
	s.proxyIPCJSON(w, r, http.MethodGet, path, "pipeline-get", nil)
}

// handlePipelinesRun invokes a saved pipeline. Sidecar injects
// X-Crewship-Invoking-{Crew,Agent} headers so the journal entries
// the executor emits record who triggered the run — that's how the
// Graph view distinguishes Crew B → Crew A's pipeline from a
// user-driven run from the UI.
//
// POST /pipelines/{slug}/run
func (s *Server) handlePipelinesRun(w http.ResponseWriter, r *http.Request, slug string) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	var body pipelinesRunRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	bodyJSON, err := json.Marshal(map[string]any{"inputs": body.Inputs})
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "marshal body"})
		return
	}
	suffix := "/run"
	if body.DryRun {
		suffix = "/dry_run"
	}
	path := "/api/v1/workspaces/" + s.ipc.WorkspaceID + "/pipelines/" + slug + suffix
	// Inject invoker identity headers — captured by the public Run
	// handler and threaded into RunInput.InvokingCrewID /
	// InvokingAgentID. Without them, the executor records the run
	// as "user-driven" which loses the cross-crew-reuse signal.
	r.Header.Set("X-Crewship-Invoking-Crew", s.ipc.CrewID)
	r.Header.Set("X-Crewship-Invoking-Agent", s.ipc.AgentID)
	s.proxyIPCJSON(w, r, http.MethodPost, path, "pipeline-run", bodyJSON)
}

// handlePipelinesDryRun is the explicit dry-run endpoint. The
// /pipelines/{slug}/run endpoint also accepts dry_run=true in body,
// but a dedicated path matches the standard "dry-run as a separate
// verb" convention so agents can guess the URL.
//
// POST /pipelines/{slug}/dry_run
func (s *Server) handlePipelinesDryRun(w http.ResponseWriter, r *http.Request, slug string) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	var body pipelinesRunRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
	}
	bodyJSON, _ := json.Marshal(map[string]any{"inputs": body.Inputs})
	path := "/api/v1/workspaces/" + s.ipc.WorkspaceID + "/pipelines/" + slug + "/dry_run"
	s.proxyIPCJSON(w, r, http.MethodPost, path, "pipeline-dry-run", bodyJSON)
}

// slugifyForPipelines converts an agent-supplied name into a
// pipelines.slug. Mirrors the kebab-case rules in
// internal/pipeline/dsl.go (slugRE) so a slug accepted here passes
// the DSL validator on the other end.
func slugifyForPipelines(name string) string {
	var out []rune
	prevHyphen := true // true at start so leading punctuation drops
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z':
			out = append(out, r)
			prevHyphen = false
		case r >= '0' && r <= '9':
			out = append(out, r)
			prevHyphen = false
		case r == '-' || r == '_':
			out = append(out, r)
			prevHyphen = true
		case r == ' ' || r == '.' || r == '/' || r == ':':
			if !prevHyphen {
				out = append(out, '-')
				prevHyphen = true
			}
		default:
			// drop other punctuation
		}
	}
	// trim trailing hyphens
	for len(out) > 0 && (out[len(out)-1] == '-' || out[len(out)-1] == '_') {
		out = out[:len(out)-1]
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// ipcResponse is the result of an internal request via the IPC
// channel. We use a custom helper here (rather than reusing
// proxyIPCJSON) because handlePipelinesSave needs to inspect the
// test_run response body before forwarding to save — the public
// proxyIPCJSON streams the response straight to the client.
type ipcResponse struct {
	status int
	body   []byte
}

// ipcRequestJSON makes an internal API call and returns the raw
// response body + status. Mirrors proxyIPCJSON but does NOT write
// to the client — callers can inspect the response and choose
// whether to forward, retry, or fan-out to a follow-up call.
//
// 15-second timeout matches proxyIPCJSON; pipeline test runs that
// exceed it surface as "test_run forward: context deadline" to the
// agent, which is correct (the agent should split the pipeline).
func (s *Server) ipcRequestJSON(ctx context.Context, method, path string, body []byte) (*ipcResponse, error) {
	if s.ipc == nil {
		return nil, fmt.Errorf("IPC not configured")
	}
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(rctx, method, s.ipc.BaseURL+path, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call: %w", err)
	}
	defer resp.Body.Close()

	// Bound the read: internal IPC payloads are small structured JSON;
	// 10 MiB is well above anything legitimate but caps a runaway peer.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	return &ipcResponse{status: resp.StatusCode, body: respBody}, nil
}
