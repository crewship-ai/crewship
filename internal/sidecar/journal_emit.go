package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// buildEgressObserver returns a proxy-side egress hook that emits one
// network.egress journal entry per allowed outbound HTTP request. The
// emit is asynchronous so the proxy hot path stays cheap — a saturated
// journal endpoint or transient crewshipd restart doesn't back-pressure
// agent traffic.
//
// This design choice is deliberate: the live audit signal the user cares
// about ("my agent called X") is already captured by the proxy logger.
// The journal entry is the persistence + UI surface, which can tolerate
// being a few milliseconds behind the wire.
func (s *Server) buildEgressObserver() EgressObserver {
	return func(host, method, provider string, statusCode int) {
		// Drop the emit entirely when IPC isn't configured — we have no
		// way to reach crewshipd, and a background goroutine that fails
		// 20 req/sec would fill the sidecar logs for no reason.
		if s == nil || s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.WorkspaceID == "" {
			return
		}

		// Spawn a small goroutine so the proxy RoundTrip path returns
		// immediately. The closure captures only stable values (host,
		// method, provider, statusCode) plus s, so there's no lifetime
		// issue with the request body or response.
		go func(host, method, provider string, status int) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			payload := map[string]any{
				"host":        host,
				"method":      method,
				"status_code": status,
			}
			if provider != "" {
				payload["provider"] = provider
			}

			summary := fmt.Sprintf("%s %s → %d", method, host, status)
			if status == 0 {
				summary = fmt.Sprintf("%s %s → transport error", method, host)
			}
			s.emitJournal(ctx, "network.egress", summary, payload, nil)
		}(host, method, provider, statusCode)
	}
}

// journalEmitRequest mirrors the wire format defined by the crewshipd
// handler in internal/api/internal_journal.go. Kept as a private struct
// here so a breaking change on either side surfaces as a compile error
// instead of a silent serialization mismatch.
type journalEmitRequest struct {
	WorkspaceID string         `json:"workspace_id"`
	CrewID      string         `json:"crew_id"`
	AgentID     string         `json:"agent_id"`
	MissionID   string         `json:"mission_id,omitempty"`
	Type        string         `json:"type"`
	Summary     string         `json:"summary"`
	Payload     map[string]any `json:"payload,omitempty"`
	Refs        map[string]any `json:"refs,omitempty"`
}

// emitJournal posts a single journal entry to crewshipd over IPC. The
// sidecar itself has no DB access (security boundary: sidecar runs
// agent-adjacent, DB writes must go through the trusted plane), so every
// observability emit takes this round-trip.
//
// The emit is fire-and-forget from the caller's perspective: we swallow
// transport errors so a down crewshipd never blocks agent outbound
// traffic. Success/failure is logged at debug level; a persistent drop
// would surface as missing Crow's Nest entries, which is the right tier
// of signal to catch a broken pipeline.
//
// The caller's ctx is used so a cancelled outer request doesn't leak a
// background HTTP call. We do NOT timeout aggressively here because
// ipcClient already carries a 30s overall timeout — stacking another
// timeout would hide real slowness behind early cancellation.
func (s *Server) emitJournal(ctx context.Context, entryType, summary string, payload, refs map[string]any) {
	if s == nil || s.ipc == nil || s.ipc.BaseURL == "" {
		return
	}
	// Drop events before the sidecar's identity is fully populated.
	// IPCConfig is injected at startup via stdin JSON; a partially
	// populated config (e.g. missing WorkspaceID) means we'd emit an
	// invalid entry that crewshipd would reject anyway.
	if s.ipc.WorkspaceID == "" {
		return
	}

	body, err := json.Marshal(journalEmitRequest{
		WorkspaceID: s.ipc.WorkspaceID,
		CrewID:      s.ipc.CrewID,
		AgentID:     s.ipc.AgentID,
		Type:        entryType,
		Summary:     summary,
		Payload:     payload,
		Refs:        refs,
	})
	if err != nil {
		// json.Marshal on map[string]any can fail if callers stuff in
		// non-JSON-able values (channels, functions); treat as a no-op
		// since this path is observability, not correctness.
		s.logger.Debug("journal emit marshal failed", "err", err, "type", entryType)
		return
	}

	// Short, dedicated timeout: network.egress is emitted inline with
	// the proxy forward path, so blocking more than ~2s per request
	// would be noticeable tail latency for the agent.
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		s.ipc.BaseURL+"/api/v1/internal/journal/emit", bytes.NewReader(body))
	if err != nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Debug("journal emit IPC failed", "err", err, "type", entryType)
		return
	}
	// Drain + close so the HTTP transport can reuse the connection.
	// We don't care about the response body — crewshipd returns the
	// generated id, but the sidecar has nowhere to use it.
	_ = resp.Body.Close()
}
