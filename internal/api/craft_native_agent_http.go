package api

import (
	"io"
	"net/http"
)

func (m *craftNativeAgentManager) task(w http.ResponseWriter, r *http.Request) {
	// No SQL work may be accepted without an actual configured private runtime.
	if m == nil || !m.enabled || m.config.AgentApply == nil || m.config.AgentInspect == nil {
		replyError(w, http.StatusServiceUnavailable, "native Craft agents unavailable")
		return
	}
	if _, err := craftNativeHuman(r.Context()); err != nil {
		replyError(w, 403, "native Craft agent task denied")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		replyError(w, 413, "native Craft agent task too large")
		return
	}
	input, err := parseCraftNativeAgentTask(raw)
	if err != nil {
		replyError(w, 400, "invalid native Craft whole-document consent")
		return
	}
	// Runtime dispatch wiring is installed only after startup qualification.
	if !m.runtimeReady() {
		replyError(w, 503, "native Craft private runtime unavailable")
		return
	}
	accepted, err := m.produceTask(r.Context(), r.PathValue("slug"), input)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	m.hint(accepted.WorkID)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, map[string]string{"work_id": accepted.WorkID, "status": "queued"})
}

// These remain closed until the actual dispatcher/runtime is attached.
func (m *craftNativeAgentManager) runtimeReady() bool {
	if m == nil || !m.enabled || m.ctx.Err() != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runtime != nil && m.dispatcher != nil
}
func (m *craftNativeAgentManager) hint(id string) {
	m.mu.Lock()
	d := m.dispatcher
	m.mu.Unlock()
	if d != nil {
		d.Hint(id)
	}
}
