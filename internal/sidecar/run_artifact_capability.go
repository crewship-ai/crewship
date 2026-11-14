package sidecar

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
)

// A run without a human (#5317 step 5, #5339) reaches artifacts only with a
// capability crewshipd minted for that one run and recorded before the agent
// started (internal/artifactrun). The orchestrator hands it to THIS sidecar in
// the run's preflight, before the agent process exists, and the sidecar
// attaches it to that run's artifact tool calls only. It never enters the
// agent's environment, its argv, or any file in the container.
//
// The registration is authenticated by the run's own token, like
// /agent/run/end: a caller can only attach a capability to a run it already
// holds the credential for. It is set once: a later registration with a
// different value is refused, so an agent that learns its own run token after
// it starts cannot replace what the host registered (the API would refuse a
// replacement anyway, because it compares against the digest it stored).

// runCapabilityHeader mirrors internal/artifactrun.Header; both sides spell it
// out to keep the sidecar free of server packages.
const runCapabilityHeader = "X-Crewship-Run-Capability"

var runCapabilityShape = regexp.MustCompile(`^urcv1\.[0-9a-f]{64}$`)

type runCapabilities struct {
	mu   sync.Mutex
	runs map[string]string
}

func (c *runCapabilities) set(runID, capability string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runs == nil {
		c.runs = map[string]string{}
	}
	if existing, ok := c.runs[runID]; ok {
		return existing == capability
	}
	c.runs[runID] = capability
	return true
}

func (c *runCapabilities) get(runID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runs[runID]
}

func (c *runCapabilities) forget(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, runID)
}

// handleRunArtifactCapability registers the caller's own run's capability.
func (s *Server) handleRunArtifactCapability(w http.ResponseWriter, r *http.Request) {
	_, _, runID, present, ok := s.actingRunIdentity(r)
	switch {
	case !present:
		writeRunEndError(w, http.StatusUnauthorized, "a bearer token is required")
		return
	case !ok:
		writeRunEndError(w, http.StatusForbidden, "token is not valid for a live run")
		return
	case runID == "":
		writeRunEndError(w, http.StatusBadRequest, "this token binds no run")
		return
	}
	capability := r.Header.Get(runCapabilityHeader)
	if !runCapabilityShape.MatchString(capability) {
		writeRunEndError(w, http.StatusBadRequest, "a run capability is required")
		return
	}
	if !s.runCapabilities.set(runID, capability) {
		writeRunEndError(w, http.StatusConflict, "this run already has a capability")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"run_id": runID, "registered": true})
}
