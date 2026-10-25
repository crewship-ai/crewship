package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/work"
)

// Lookup DATA only: every tool still enters the original mandate's current
// authority boundary. Neither a restored run row nor a bearer creates an entry.
type collaborationAgentTaskController struct {
	mu         sync.Mutex
	manager    *collaborationHTTPManager
	handler    *WebhookHandler
	tasks      *collaborationAgentTaskDispatcher
	dispatcher *dispatch.Dispatcher
	confirmed  map[string]*collaborationLiveAgent
}

// This qualifies the one supported existing execution path. The later
// Orchestrator guard verifies the ACTUAL computed exec, run HOME/config and
// live sidecar immediately before its original process creation gate.
type collaborationDocumentEnvironment struct{ handler *WebhookHandler }

func (e collaborationDocumentEnvironment) BeforeStart(ctx context.Context) error {
	if ctx.Err() != nil || e.handler == nil || e.handler.container == nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	if _, ok := e.handler.orch.(*orchestrator.Orchestrator); !ok {
		return orchestrator.ErrPrivateRunUnqualified
	}
	reporter, ok := e.handler.container.(interface{ ExpectedSidecarHash() string })
	if !ok || reporter.ExpectedSidecarHash() == "" {
		return orchestrator.ErrPrivateRunUnqualified
	}
	return nil
}
func (e collaborationDocumentEnvironment) Qualify(ctx context.Context, info chatbridge.ChatInfo, use *provider.RuntimeUse) error {
	if e.BeforeStart(ctx) != nil || info.CLIAdapter != "CLAUDE_CODE" || use == nil || !use.Matches(info.CrewID, use.ContainerID()) {
		return orchestrator.ErrPrivateRunUnqualified
	}
	return nil
}

func (m *collaborationHTTPManager) startAgentTasks(h *WebhookHandler) error {
	if m == nil || h == nil || h.db != m.pages.db || (collaborationDocumentEnvironment{h}).BeforeStart(m.ctx) != nil {
		return orchestrator.ErrPrivateRunUnqualified
	}
	mandates, err := newCollaborationAgentMandateStore(m.pages, true, m.fence, m.clock, time.Hour, 100)
	if err != nil {
		return err
	}
	tasks, err := newCollaborationAgentTaskDispatcher(mandates)
	if err != nil {
		return err
	}
	c := &collaborationAgentTaskController{manager: m, handler: h, tasks: tasks, confirmed: make(map[string]*collaborationLiveAgent)}
	runtime, err := newCollaborationAgentTaskRuntime(tasks, h, collaborationDocumentEnvironment{h}, c.attach)
	if err != nil {
		return err
	}
	runtime.documentTools = true
	runtime.onRetired = c.retire
	c.dispatcher = runtime.NewDispatcher(dispatch.Config{Owner: "mooring-document-dispatcher", Limits: work.SerialAgentLimits(), PollInterval: 2 * time.Second, ConfirmPollInterval: time.Second, Paused: quiesce.QueuePaused})
	m.mu.Lock()
	if m.closed || m.agentTasks != nil || m.agentMandates != nil {
		m.mu.Unlock()
		return errCollaborationAgentAccess
	}
	m.agentMandates, m.agentTasks = mandates, c
	m.workers.Add(1)
	m.mu.Unlock()
	finish := beginBackgroundWork()
	go func() { defer finish(); defer m.workers.Done(); _ = c.dispatcher.Run(m.ctx) }()
	return nil
}

func (c *collaborationAgentTaskController) retire(ctx context.Context, run, exec string) error {
	c.mu.Lock()
	a := c.confirmed[run]
	c.mu.Unlock()
	if a == nil || a.confirmed.execID != exec || a.confirmed.runID != run || a.confirmed.store != c.manager.agentMandates {
		return errCollaborationAgentAccess
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if err := a.closeConfirmedRun(ctx); err != nil {
		return err
	}
	s := a.confirmed.store
	s.mu.Lock()
	entry, ok := s.attached[a.confirmed.mandate]
	if ok && entry.binding.attempt.runID == run && entry.binding.attempt.workID == a.confirmed.workID && entry.binding.attempt.generation == a.confirmed.generation {
		delete(s.attached, a.confirmed.mandate)
	}
	s.mu.Unlock()
	c.mu.Lock()
	if c.confirmed[run] == a {
		delete(c.confirmed, run)
	}
	c.mu.Unlock()
	return nil
}

func (c *collaborationAgentTaskController) attach(ctx context.Context, confirmed collaborationAgentConfirmed) error {
	m := c.manager
	m.mu.Lock()
	d := m.docs[confirmed.key]
	valid := !m.closed && confirmed.store == m.agentMandates && d != nil && d.cell != nil && d.activation != nil
	m.mu.Unlock()
	if !valid || ctx.Err() != nil || confirmed.runID == "" || confirmed.execID == "" {
		return errCollaborationAgentAccess
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.confirmed) >= 100 {
		return mooring.ErrBatchCapacity
	}
	if _, exists := c.confirmed[confirmed.runID]; exists {
		return errCollaborationAgentAccess
	}
	ref, err := collaborationOpaqueID()
	if err != nil {
		return err
	}
	a := &collaborationLiveAgent{document: d, confirmed: confirmed, ref: ref, ready: make(chan struct{})}
	d.mu.Lock()
	if d.closed || len(d.agents)+len(d.connections) >= collaborationHTTPParticipantLimit {
		d.mu.Unlock()
		return mooring.ErrBatchCapacity
	}
	if d.agents == nil {
		d.agents = make(map[string]*collaborationLiveAgent)
	}
	d.agents[ref] = a
	d.mu.Unlock()
	c.confirmed[confirmed.runID] = a
	return nil
}

func (m *collaborationHTTPManager) task(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m.mu.Lock()
	c := m.agentTasks
	valid := !m.closed && c != nil && m.agentMandates != nil
	m.mu.Unlock()
	if !valid {
		replyError(w, 503, "document task runtime unavailable")
		return
	}
	key, rec, err := m.scope(r)
	if err != nil {
		replyError(w, 403, "document task denied")
		return
	}
	if !m.pages.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		replyError(w, 413, "document task request too large")
		return
	}
	canonical, err := mooring.CanonicalJSON(raw)
	var fields map[string]json.RawMessage
	var input struct {
		ChatID    string `json:"chat_id"`
		MessageID string `json:"message_id"`
	}
	if err != nil || json.Unmarshal(canonical, &fields) != nil || len(fields) != 2 || fields["chat_id"] == nil || fields["message_id"] == nil || json.Unmarshal(canonical, &input) != nil || !mooring.ValidWireIdentifier(input.ChatID) || !mooring.ValidWireIdentifier(input.MessageID) {
		replyError(w, 400, "invalid document task request")
		return
	}
	source := auth.OriginalCredentialFromContext(r.Context())
	if source == nil {
		replyError(w, 403, "document task denied")
		return
	}
	receipt, err := chataudience.CaptureTrusted(r.Context(), m.pages.db, input.ChatID, source.UserID, nil)
	if err != nil || receipt == nil || receipt.WorkspaceID != key.WorkspaceID {
		replyError(w, 403, "document task denied")
		return
	}
	info, err := c.handler.resolver.ResolveChat(chataudience.WithReceipt(r.Context(), receipt), input.ChatID)
	if err != nil || info == nil || info.CLIAdapter != "CLAUDE_CODE" || info.AgentID != receipt.AgentID || info.WorkspaceID != key.WorkspaceID {
		replyError(w, 503, "supported private document runtime required")
		return
	}
	// Existing native bootstrap/identity validation is mandatory. A picker row
	// or application label is not evidence that the document is qualified D1.
	if _, err = m.document(r, key, nil); err != nil {
		replyError(w, 503, "native document unavailable")
		return
	}
	accepted, err := c.tasks.Produce(r.Context(), key, "task", input.MessageID, receipt)
	if err != nil {
		replyError(w, 409, "document task not accepted; use a new explicit message")
		return
	}
	c.dispatcher.Hint(accepted.WorkID)
	writeJSON(w, http.StatusAccepted, map[string]string{"work_id": accepted.WorkID, "status": "queued"})
}
