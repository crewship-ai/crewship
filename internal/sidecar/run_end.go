package sidecar

import (
	"encoding/json"
	"net/http"
)

// handleRunEnd marks the CALLER'S OWN run finished, after which its token stops
// authenticating (identityForRunToken → runs.current). It takes no body and no
// parameters: the run it ends is the one its bearer token is bound to.
//
// Self-service by construction, and that is the security argument. A caller can
// only end a run it already holds the credential for, so the worst it can do is
// end itself — which is what the orchestrator is asking for anyway, and which
// an agent could achieve by exiting. There is no "end that other run" verb to
// abuse, and none is needed: the orchestrator holds each run's own token.
//
// Refuses a v1 token (present, resolves, but binds no run) rather than guessing
// which run the caller meant. Refuses an already-ended run's token too — that
// request is either a retry, which is harmless and idempotent from the
// registry's side, or a zombie, which must not be answered as if it were live.
//
// Idempotent: repeating it is a no-op, because the orchestrator retries and a
// second end must not look like a failure.
func (s *Server) handleRunEnd(w http.ResponseWriter, r *http.Request) {
	agentID, _, runID, present, ok := s.actingRunIdentity(r)
	if !present {
		writeRunEndError(w, http.StatusUnauthorized, "a bearer token is required")
		return
	}
	if !ok {
		// Either a forgery, or a run already ended. Both are refusals, and
		// the registry state is already what the caller wanted in the second
		// case — so say so without leaking which it was.
		writeRunEndError(w, http.StatusForbidden, "token is not valid for a live run")
		return
	}
	if runID == "" {
		writeRunEndError(w, http.StatusBadRequest,
			"this token binds no run; only a per-run token can end a run")
		return
	}
	s.runs.end(runID)
	s.runs.sweep()
	if s.logger != nil {
		s.logger.Info("run marked ended; its token no longer authenticates",
			"run_id", runID, "agent_id", agentID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"run_id": runID, "ended": true})
}

func writeRunEndError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
