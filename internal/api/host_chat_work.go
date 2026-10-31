package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/work"
)

// hostChatRunProof is an IPC control field, never model-supplied metadata.
// Container/crew tokens may not use it to settle host-owned executions.
type hostChatRunProof struct {
	Phase        string `json:"phase"`
	ObservedExit *int   `json:"observed_exit,omitempty"`
	ExecRefused  bool   `json:"exec_refused,omitempty"`
}

func (h *InternalHandler) beginHostChatWork(w http.ResponseWriter, r *http.Request, request work.HostChatRequest) (managed, fresh bool, ok bool) {
	store := work.NewStore(h.db)
	projection, owned, err := store.RunProjection(r.Context(), request.RunID)
	if err != nil {
		replyInternalError(w, h.logger, "read run ownership", err)
		return false, false, false
	}
	if owned {
		if projection.WorkspaceID != request.WorkspaceID || projection.AgentID != request.AgentID || projection.CrewID != request.CrewID {
			replyError(w, 409, "run work identity mismatch")
			return false, false, false
		}
		_, managed, err = store.HostChatAttempt(r.Context(), request.RunID)
		if err != nil {
			replyError(w, 409, "run attempt is not current")
			return false, false, false
		}
		if !managed {
			return false, false, true
		} // Existing dispatcher remains its owner.
	}
	_, fresh, err = store.BeginHostChat(r.Context(), request)
	if err != nil {
		if errors.Is(err, work.ErrNoWork) {
			replyError(w, 429, "chat execution capacity unavailable")
		} else if errors.Is(err, work.ErrNotBound) || errors.Is(err, work.ErrStaleGeneration) {
			replyError(w, 409, "run attempt identity is not current")
		} else {
			replyInternalError(w, h.logger, "admit host chat work", err)
		}
		return false, false, false
	}
	return true, fresh, true
}

func (h *InternalHandler) applyHostChatWorkProof(w http.ResponseWriter, r *http.Request, runID, status, started, completed string, exit *int, message *string, raw json.RawMessage, proof *hostChatRunProof) bool {
	if proof == nil {
		return false
	}
	if InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		replyError(w, 403, "host run lifecycle requires host authentication")
		return true
	}
	store := work.NewStore(h.db)
	_, owned, err := store.HostChatAttempt(r.Context(), runID)
	if err != nil || !owned {
		replyError(w, 409, "host run attempt unavailable")
		return true
	}
	switch proof.Phase {
	case "request", "started", "heartbeat":
		if status != "RUNNING" {
			replyError(w, 400, "execution phase requires RUNNING status")
			return true
		}
		if proof.Phase == "request" {
			binding, _, lookupErr := store.HostChatAttempt(r.Context(), runID)
			if lookupErr != nil {
				replyError(w, 409, "run attempt unavailable")
				return true
			}
			tx, beginErr := h.db.BeginTx(r.Context(), nil)
			if beginErr != nil {
				replyInternalError(w, h.logger, "lock host execution authority", beginErr)
				return true
			}
			defer tx.Rollback()
			if _, authorityErr := runauthority.Resolve(r.Context(), tx, runID, binding.AgentID, binding.WorkspaceID); authorityErr != nil {
				replyError(w, 403, "original chat authority is no longer current")
				return true
			}
			if err = work.MarkRuntimeRequestedTx(r.Context(), tx, binding.WorkID, runID, binding.Generation); err != nil {
				replyError(w, 409, "host execution is no longer awaiting creation")
				return true
			}
			if err = tx.Commit(); err != nil {
				replyInternalError(w, h.logger, "commit host execution authority", err)
				return true
			}
			writeJSON(w, 200, map[string]string{"id": runID, "status": "RUNNING"})
			return true
		}
		if err = store.HostChatPhase(r.Context(), runID, proof.Phase); err != nil {
			replyError(w, 409, "host run attempt is no longer current")
			return true
		}
		writeJSON(w, 200, map[string]string{"id": runID, "status": "RUNNING"})
		return true
	case "finish":
		if status == "RUNNING" {
			replyError(w, 400, "finish requires a terminal status")
			return true
		}
		startTime, parseErr := time.Parse(time.RFC3339Nano, started)
		if parseErr != nil {
			replyError(w, 500, "invalid original run timestamp")
			return true
		}
		if _, err = journal.RunTerminalEventTime(startTime, completed); err != nil {
			replyError(w, 400, "invalid completion time")
			return true
		}
		var metadata map[string]any
		if len(raw) > 0 {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err = decoder.Decode(&metadata); err != nil {
				replyError(w, 400, "invalid run metadata")
				return true
			}
		}
		err = store.FinishHostChat(r.Context(), runID, work.HostChatResult{Status: status, ObservedExit: proof.ObservedExit, ExecRefused: proof.ExecRefused, Result: work.RunResult{CompletedAt: completed, ExitCode: exit, ErrorMessage: message, Metadata: metadata}})
		if err != nil {
			replyError(w, 409, "host run terminal ownership is unconfirmed")
			return true
		}
		return false // The normal projection/journal path owns the visible receipt.
	default:
		replyError(w, 400, "unknown host run lifecycle phase")
		return true
	}
}
