package sidecar

// File: coordinator.go — sidecar handlers that proxy workspace-wide
// crewshipd endpoints (crews, crew-connections, credentials, agent
// creation, issue creation). Lead and AGENT agents reach these via the
// sidecar so they don't need direct network access to the host API.
//
// Historical note: the file is named after the now-removed COORDINATOR
// role, which originally drove these endpoints. The proposal +
// missions/all proxy handlers that were COORDINATOR-only have been
// removed. Rename pending — kept as-is in v0.1 to keep the diff focused.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

// maxProxyBodyBytes caps every body the sidecar will buffer when
// forwarding a workspace-scoped POST/PATCH/PUT upstream to crewshipd.
// The sidecar runs as UID 1002 inside the agent container — the agent
// process (UID 1001) is what writes to these endpoints, and a buggy or
// adversarial agent could otherwise OOM the sidecar by streaming a
// multi-GB body. 1 MiB matches the internal/api/readJSON cap on the
// destination handler so legitimate workspace-scoped payloads (agent
// records, credential metadata) always fit comfortably.
const maxProxyBodyBytes = 1 << 20

// handleListCrews proxies GET /crews to the crewshipd internal API.
// Used by AGENT and LEAD agents to discover all workspace crews.
func (s *Server) handleListCrews(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crews?workspace_id="+s.ipc.WorkspaceID)
}

// handleListCrewConnections proxies GET /crew-connections to the crewshipd API.
// Used by AGENT and LEAD agents to discover crew connection topology.
func (s *Server) handleListCrewConnections(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodGet, "/api/v1/internal/crew-connections?workspace_id="+s.ipc.WorkspaceID)
}

// COORDINATOR-only proposal + cross-mission proxy handlers were removed in
// v0.1 (handleCreateProposal, handleListProposals, handleListAllMissions,
// handleAllMissionsSummary). The matching public + internal API routes are
// also gone — see internal/api/proposals.go deletion. v0.2 will replace
// the proposal flow with a crew-to-crew handoff primitive; recover the
// reference implementation from git history at that point.

// handleListCredentials proxies GET /credentials to the crewshipd internal API.
// Used by AGENT and LEAD agents to discover available credentials for assignment.
//
// Post-Patch-A crewshipd already returns metadata only by default, but we
// also strip any access_token / refresh_token that survives a future
// crewshipd regression — defense in depth: an agent inside the container
// never sees plaintext via this path even if the upstream handler
// accidentally re-introduces the field. The sidecar's stdin boot payload
// (orchestrator/exec_sidecar.go) is the ONLY supply line for cleartext
// values, and that path goes straight to the credStore — never to the
// agent over HTTP.
func (s *Server) handleListCredentials(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPIFiltered(
		w, r,
		http.MethodGet,
		"/api/v1/internal/credentials?workspace_id="+s.ipc.WorkspaceID,
		stripCredentialValues,
	)
}

// stripCredentialValues removes plaintext token fields from a credentials
// list response before it goes back to the agent. Operates on a generic
// []map[string]any so a future crewshipd schema change that adds new
// secret-looking fields doesn't silently bypass the filter.
//
// Fields scrubbed (case-sensitive, JSON tag names): access_token,
// refresh_token, encrypted_value, encrypted_refresh_token, token,
// secret. Both top-level and nested under arbitrary maps.
func stripCredentialValues(raw json.RawMessage) json.RawMessage {
	// Try array shape first (the canonical /credentials response).
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, row := range arr {
			scrubSecretKeys(row)
		}
		out, _ := json.Marshal(arr)
		return out
	}
	// Fall through: try object shape (error envelopes etc.). If we can't
	// parse it at all, pass it through unchanged — wrapper errors carry
	// no credential data.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		scrubSecretKeys(obj)
		out, _ := json.Marshal(obj)
		return out
	}
	return raw
}

// secretKeyDenylist is the closed set of JSON keys the sidecar will refuse
// to relay back to the agent. Conservative — agent-visible metadata
// (id, name, type, provider, status, created_at, etc.) is NOT in the list.
var secretKeyDenylist = map[string]struct{}{
	"access_token":            {},
	"refresh_token":           {},
	"encrypted_value":         {},
	"encrypted_refresh_token": {},
	"token":                   {},
	"secret":                  {},
	"plain_value":             {},
}

func scrubSecretKeys(m map[string]any) {
	for k, v := range m {
		if _, banned := secretKeyDenylist[k]; banned {
			delete(m, k)
			continue
		}
		switch nested := v.(type) {
		case map[string]any:
			scrubSecretKeys(nested)
		case []any:
			for _, item := range nested {
				if im, ok := item.(map[string]any); ok {
					scrubSecretKeys(im)
				}
			}
		}
	}
}

// handleAssignAgentCredential proxies POST /agent-credentials to the crewshipd internal API.
// Used by LEAD agents to assign a credential to a newly created agent.
func (s *Server) handleAssignAgentCredential(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/agent-credentials?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateCrewConnection proxies POST /crew-connections to the crewshipd internal API.
// Used by LEAD agents to connect a new crew to existing ones.
func (s *Server) handleCreateCrewConnection(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/crew-connections?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateCrew proxies POST /crew/create to the crewshipd internal API.
// Allows LEAD agents to create new crews in the workspace.
func (s *Server) handleCreateCrew(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/crews?workspace_id="+s.ipc.WorkspaceID)
}

// handleCreateAgent proxies POST /agent/create to the crewshipd internal API.
// Allows LEAD agents to create new agents within a crew.
func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}
	s.proxyToAPI(w, r, http.MethodPost, "/api/v1/internal/agents?workspace_id="+s.ipc.WorkspaceID)
}

// proxyToAPI is a generic helper that proxies a request to the crewshipd internal API.
func (s *Server) proxyToAPI(w http.ResponseWriter, r *http.Request, method, path string) {
	s.proxyToAPIFiltered(w, r, method, path, nil)
}

// proxyToAPIFiltered is the same as proxyToAPI but applies a transform to
// the response body before relaying it back to the agent. Used by
// handleListCredentials to strip plaintext token fields even if a future
// crewshipd handler regresses and emits them.
func (s *Server) proxyToAPIFiltered(
	w http.ResponseWriter,
	r *http.Request,
	method, path string,
	transform func(json.RawMessage) json.RawMessage,
) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var bodyReader io.Reader
	if r.Body != nil && (method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut) {
		// MaxBytesReader closes the underlying body on overflow and
		// returns a sentinel *http.MaxBytesError on the next Read.
		limited := http.MaxBytesReader(w, r.Body, maxProxyBodyBytes)
		bodyBytes, err := io.ReadAll(limited)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeJSONResponse(w, http.StatusRequestEntityTooLarge,
					map[string]string{"error": "request body exceeds sidecar limit"})
				return
			}
			writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "failed to read body"})
			return
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.ipc.BaseURL+path, bodyReader)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to create request"})
		return
	}
	req.Header.Set("X-Internal-Token", s.ipc.Token)
	if bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// Propagate end-user attribution through the sidecar so the backend
	// handler can dual-path: user-attributed (capability check) vs.
	// autonomous-agent (autonomy_level check). Headers are pass-through
	// — the chat-bridge / CLI repl sets them on the inbound request when
	// the action was end-user-initiated (slash command); autonomous-agent
	// tool calls leave them empty and the backend falls back to the
	// autonomy gate. Source is informational for audit (chat-ui vs cli-
	// repl vs anything-else) and never gates behaviour by itself.
	//
	// See PRD-SLASH-CAPABILITIES-2026.md §6.3 — these headers are the
	// dual-path discriminator the backend handler reads via
	// CallerUserIDFromRequest (internal/api/caller_identity.go).
	//
	// === SECURITY / TRUST BOUNDARY ===
	//
	// The sidecar listens on 127.0.0.1:9119 *inside* the agent's own
	// container's network namespace. Anything that can hit this port is
	// inside the same container — i.e. the agent process and any tooling
	// it launched. By design that includes potentially-malicious agent
	// output (e.g. a tool call that constructs an arbitrary HTTP request
	// against localhost). A compromised agent could set its OWN
	// X-Caller-User-Id header to a different user's id and the backend
	// would log + audit the operation as that other user.
	//
	// The mitigations are operational, not protocol-level:
	//
	//   - Inbound listeners (handleHTTP / handleLocal / proxy.go:308)
	//     already verify the request came over loopback AND the TCP peer
	//     address is also loopback (remoteIsLoopback) so a peer crew on
	//     the same Docker bridge can't reach this port via Host-header
	//     spoofing.
	//   - Capability-gated endpoints (commit 6) only TRUST the header in
	//     the slash-command path; autonomous tool calls go through a
	//     SEPARATE backend route family that ignores the header and gates
	//     on the crew's autonomy_level instead.
	//   - The chat-bridge / CLI repl are the only legit setters; both run
	//     OUTSIDE the agent container, attach the header before sending
	//     to the sidecar, and the agent process can't intercept that
	//     in-flight (different namespace).
	//
	// ID1 HARDENING (PRD §11, formerly out-of-scope, now implemented):
	// the X-Caller-User-Id header is attacker-influenceable — the agent
	// process can construct a request to this sidecar over loopback and
	// set any user id it likes. To stop the backend from trusting a
	// forged identity for privileged credential mutation we attach an
	// HMAC signature keyed by the workspace-bound internal token
	// (s.ipc.Token). That token lives only in the sidecar process
	// (UID 1002); the agent (UID 1001) never holds it, so the agent
	// cannot produce a valid signature itself. The backend re-derives
	// the MAC from the same token (validated by the internal-auth
	// middleware) and constant-time-compares before honouring the
	// header — see internal/api/internal_credentials_mutate.go. The
	// signature binds the caller id to THIS sidecar's workspace, so a
	// captured signature can't be replayed against another tenant.
	if callerID := r.Header.Get("X-Caller-User-Id"); callerID != "" {
		req.Header.Set("X-Caller-User-Id", callerID)
		if sig := internaltoken.SignCaller(s.ipc.Token, s.ipc.WorkspaceID, callerID); sig != "" {
			req.Header.Set("X-Caller-Signature", sig)
		}
	}
	if callerSrc := r.Header.Get("X-Caller-Source"); callerSrc != "" {
		req.Header.Set("X-Caller-Source", callerSrc)
	}

	resp, err := ipcClient.Do(req)
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "crewshipd request failed"})
		return
	}
	defer resp.Body.Close()

	var result json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{"error": "invalid response"})
		return
	}

	if transform != nil {
		result = transform(result)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(result)
}
