package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// allowedSidecarEntryTypes is the closed set of journal entry types the
// sidecar is permitted to emit through the IPC endpoint. We deliberately
// restrict this rather than accepting arbitrary EntryType strings:
//
//  1. The sidecar lives inside the agent container trust zone. An agent
//     could coerce it into emitting assignment.completed or
//     approval.granted, which would corrupt cost accounting, mission
//     state, or the audit log.
//  2. The only legitimate sidecar-owned event types today are
//     network.egress (outbound HTTP) and file.written (inotify). Adding
//     to this allowlist is a deliberate product decision, not a handler
//     escape hatch.
//  3. Lookup is O(1) and the set is tiny, so a map suffices.
var allowedSidecarEntryTypes = map[journal.EntryType]struct{}{
	journal.EntryNetworkEgress: {},
	journal.EntryFileWritten:   {},
}

// sidecarJournalEmitRequest is the wire format the sidecar POSTs. It is
// deliberately narrower than journal.Entry: the scope fields
// (WorkspaceID/CrewID/AgentID) are authoritative because the sidecar
// learned them from its IPCConfig boot payload, which crewshipd itself
// stamped. Fields the sidecar has no business setting — ID, TS, TraceID,
// SpanID, Severity overrides above info — are not accepted.
type sidecarJournalEmitRequest struct {
	WorkspaceID string         `json:"workspace_id"`
	CrewID      string         `json:"crew_id"`
	AgentID     string         `json:"agent_id"`
	MissionID   string         `json:"mission_id,omitempty"`
	Type        string         `json:"type"`
	Summary     string         `json:"summary"`
	Payload     map[string]any `json:"payload,omitempty"`
	Refs        map[string]any `json:"refs,omitempty"`
}

// handleSidecarEmit accepts a journal entry submitted by the sidecar over
// the internal IPC channel and forwards it to the configured journal
// Emitter. Auth is provided by internalAuth() middleware (X-Internal-Token).
//
// Validation:
//   - Type must be in allowedSidecarEntryTypes; anything else → 400.
//   - Summary and WorkspaceID must be non-empty (enforced by
//     journal.Entry.Validate upstream, but we check here to return a
//     clear 400 instead of a 500).
//   - Payload size is bounded by http.MaxBytesReader at the boundary; we
//     don't trust the sidecar to self-limit.
//
// On success returns {"id": "<entry id>"} with 202 Accepted because the
// underlying journal Writer buffers entries asynchronously.
func (r *Router) handleSidecarEmit(w http.ResponseWriter, req *http.Request) {
	// 64 KiB is generous for a single journal entry (payloads are tiny
	// map[string]any blobs). Any larger indicates either a buggy sidecar
	// or an exfiltration attempt; fail fast.
	req.Body = http.MaxBytesReader(w, req.Body, 64*1024)

	var body sidecarJournalEmitRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Normalize the type and enforce the allowlist BEFORE hitting the
	// emitter; a silent write with the wrong type would poison the
	// append-only stream with events we can't retract.
	entryType := journal.EntryType(strings.TrimSpace(body.Type))
	if _, ok := allowedSidecarEntryTypes[entryType]; !ok {
		replyError(w, http.StatusBadRequest, "entry type not allowed from sidecar")
		return
	}

	if strings.TrimSpace(body.WorkspaceID) == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	if strings.TrimSpace(body.Summary) == "" {
		replyError(w, http.StatusBadRequest, "summary required")
		return
	}

	id, err := r.Journal().Emit(req.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     body.AgentID,
		MissionID:   body.MissionID,
		Type:        entryType,
		// Sidecar always reports at info severity — the allowlisted types
		// (network.egress, file.written) are observability noise, not
		// alerts. If a future type needs warn/error, pipe it through a
		// dedicated handler with stricter validation.
		Severity: journal.SeverityInfo,
		// ActorType is fixed to sidecar so downstream filters ("show only
		// agent-initiated events") can reliably exclude platform traffic.
		ActorType: journal.ActorSidecar,
		ActorID:   body.AgentID,
		Summary:   body.Summary,
		Payload:   body.Payload,
		Refs:      body.Refs,
	})
	if err != nil {
		// Validation errors from journal.Entry.Validate are the caller's
		// fault (missing workspace id etc.) and should be 400, not 500.
		// We surface the message so the sidecar can log something useful.
		// Anything else (DB down, disk full) is 500.
		if strings.HasPrefix(err.Error(), "journal:") {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		r.logger.Error("sidecar journal emit failed", "err", err, "type", entryType)
		replyError(w, http.StatusInternalServerError, "emit failed")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}
