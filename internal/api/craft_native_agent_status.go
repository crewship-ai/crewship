package api

import (
	"context"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

type craftNativeAgentToolEvent struct {
	Operation  string                     `json:"operation"`
	Status     int                        `json:"status"`
	At         string                     `json:"at"`
	HolderKind string                     `json:"holder_kind,omitempty"`
	Receipt    *mooring.NativeSaveReceipt `json:"receipt,omitempty"`
}

type nativeAgentStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *nativeAgentStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *nativeAgentStatusWriter) Write(raw []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(raw)
}

// This is an original human's private task observation, never a grant. Only
// actual host outcomes are projected; no document bytes, prompts or tokens.
func (m *craftNativeAgentManager) taskStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m == nil || !m.enabled || !m.human.enabled {
		replyError(w, 503, "native Craft agents unavailable")
		return
	}
	source, err := craftNativeHuman(r.Context())
	if err != nil {
		replyError(w, 403, "native task status denied")
		return
	}
	id := r.PathValue("workID")
	if !mooring.ValidWireIdentifier(id) {
		replyError(w, 404, "native task unavailable")
		return
	}
	tx, err := m.writer(r.Context())
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	workspace := WorkspaceIDFromContext(ctx)
	page, err := m.human.pages.loadPageForRead(ctx, workspace, r.PathValue("slug"))
	if err != nil {
		replyError(w, 403, "native task status denied")
		return
	}
	scope := mooring.EditLeaseScope{WorkspaceID: workspace, ArtifactID: page.ID}
	if _, err = m.human.pageAuthority(ctx, tx, source, scope, true); err != nil {
		replyError(w, 403, "native task status denied")
		return
	}
	m.mu.Lock()
	e := m.entries[id]
	valid := e != nil && e.seal == m.seal && e.grant.Scope == scope && e.grant.Source.UserID == source.UserID
	m.mu.Unlock()
	if !valid {
		replyError(w, 404, "native task unavailable")
		return
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM work_items WHERE id=? AND workspace_id=? AND domain_kind=? AND domain_id=? AND authorized_by_user_id=?`, id, workspace, craftNativeAgentDomain, page.ID, source.UserID).Scan(&state); err != nil {
		replyError(w, 404, "native task unavailable")
		return
	}
	result := map[string]any{"work_id": id, "state": state, "application": e.grant.Application, "agent_id": e.grant.AgentID, "events": []craftNativeAgentToolEvent{}, "operation_inflight": true, "uncertain": true, "timeline_durability": "current-host-only"}
	// A long engine call must not block a human's observation or stop control.
	if e.callMu.TryLock() {
		result["operation_inflight"] = false
		result["uncertain"] = false
		if e.tool != nil {
			result["uncertain"] = e.tool.uncertain
			events := make([]craftNativeAgentToolEvent, len(e.tool.events))
			copy(events, e.tool.events)
			for i := range events {
				if events[i].Receipt != nil {
					receipt := *events[i].Receipt
					events[i].Receipt = &receipt
				}
			}
			result["events"] = events
			if e.tool.lastReceipt != nil {
				receipt := *e.tool.lastReceipt
				result["last_committed_receipt"] = &receipt
			}
		}
		e.callMu.Unlock()
	}
	m.mu.Lock()
	result["run_id"] = e.attempt.runID
	result["grant_closed"] = e.closed
	m.mu.Unlock()
	if _, err = m.human.pageAuthority(ctx, tx, source, scope, true); err != nil {
		replyError(w, 403, "native task status denied")
		return
	}
	if err = tx.Commit(); err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func recordNativeAgentToolEvent(e *craftNativeAgentEntry, input craftNativeAgentToolRequest, event craftNativeAgentToolEvent, status int) {
	event.Status = status
	event.At = time.Now().UTC().Format(time.RFC3339Nano)
	if input.Operation == "apply" && status == 200 {
		if attempt := e.tool.requests[input.RequestID]; attempt != nil && attempt.receipt != nil {
			copy := *attempt.receipt
			event.Receipt = &copy
		}
	}
	e.tool.events = append(e.tool.events, event)
	if len(e.tool.events) > 64 {
		e.tool.events = append([]craftNativeAgentToolEvent(nil), e.tool.events[len(e.tool.events)-64:]...)
	}
}
