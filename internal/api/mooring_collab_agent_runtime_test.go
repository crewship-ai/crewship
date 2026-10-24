package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/work"
)

// Synthetic provider/executor stand-ins exercise the host/SQL bridge only.
// They supply NO native privacy, actual provider or production-auth evidence.
type collaborationRuntimeContainer struct {
	provider.ContainerProvider
	starts int
}

func (c *collaborationRuntimeContainer) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	c.starts++
	return "synthetic-private-container", nil
}

type collaborationRuntimeEnvironment struct{}

func (collaborationRuntimeEnvironment) BeforeStart(context.Context) error { return nil }
func (collaborationRuntimeEnvironment) Qualify(context.Context, chatbridge.ChatInfo, *provider.RuntimeUse) error {
	return nil
}

type collaborationRuntimeRunner struct {
	request       *orchestrator.AgentRunRequest
	beforeStarted func()
	stopCalls     int
}

func (r *collaborationRuntimeRunner) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, handler orchestrator.EventHandler) error {
	r.request = &req
	if handler != nil {
		return errors.New("generic content handler installed")
	}
	if err := req.ExecGate(ctx); err != nil {
		return err
	}
	if r.beforeStarted != nil {
		r.beforeStarted()
	}
	req.OnExecStarted("synthetic-exec")
	return nil
}
func (r *collaborationRuntimeRunner) StopRun(context.Context, string) (bool, error) {
	r.stopCalls++
	return false, nil
}
func (*collaborationRuntimeRunner) RunIsAlive(context.Context, string) (bool, error) {
	return false, errors.New("synthetic uncertain probe")
}

type collaborationRuntimeResolver struct {
	chatbridge.ChatResolver
	info     *chatbridge.ChatInfo
	handler  *InternalHandler
	original chataudience.Receipt
	message  string
}

func (r *collaborationRuntimeResolver) ResolveChat(context.Context, string) (*chatbridge.ChatInfo, error) {
	return r.info, nil
}
func (r *collaborationRuntimeResolver) CreateRun(ctx context.Context, id, agent, chat, workspace, trigger string, metadata map[string]interface{}) error {
	receipt := chataudience.ReceiptFromContext(ctx)
	if receipt == nil || *receipt != r.original || runauthority.OriginIDFromContext(ctx) != "" || runauthority.TriggerMessageFromContext(ctx) != r.message {
		return errors.New("original host provenance replaced")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/runs", jsonBody(map[string]any{"id": id, "agent_id": agent, "chat_id": chat, "workspace_id": workspace, "trigger_type": trigger, "metadata": metadata, "human_authority": receipt, "trigger_message_id": r.message})).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.handler.CreateRun(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		return fmt.Errorf("create run: HTTP %d", rec.Code)
	}
	return nil
}

func collaborationRuntimeFixture(t *testing.T) (*collaborationAgentTestFixture, *collaborationAgentTaskDispatcher, *WebhookHandler, *collaborationRuntimeRunner, *collaborationRuntimeContainer, dispatch.Assignment) {
	t.Helper()
	x := newCollaborationAgentTestFixture(t)
	d, err := newCollaborationAgentTaskDispatcher(x.store)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := d.Produce(x.human, x.key, "task", x.f.a.MessageID, x.f.receipt)
	if err != nil {
		t.Fatal(err)
	}
	store := work.NewStore(x.f.rig.db)
	claimed, err := store.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "synthetic-runtime-host", WorkID: accepted.WorkID, Limits: work.DefaultLimits()})
	if err != nil {
		t.Fatal(err)
	}
	a := dispatch.Assignment{Item: claimed.Item, RunID: claimed.RunID, Attempt: claimed.Attempt, Generation: claimed.Generation}
	if err = store.MarkStarting(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:"+a.RunID); err != nil {
		t.Fatal(err)
	}
	internal := NewInternalHandler(x.f.rig.db, "synthetic-private-ipc", newTestLogger())
	wireTestJournalForHandler(t, x.f.rig.db, internal)
	runner := &collaborationRuntimeRunner{}
	container := &collaborationRuntimeContainer{}
	resolver := &collaborationRuntimeResolver{handler: internal, original: *x.f.receipt, message: x.f.a.MessageID, info: &chatbridge.ChatInfo{AgentID: x.f.a.AgentID, AgentSlug: "principal-agent", AgentRole: "AGENT", CrewID: x.f.a.CrewID, CrewSlug: "principal-crew", WorkspaceID: x.f.a.WorkspaceID, CLIAdapter: "CLAUDE_CODE", TimeoutSecs: 5, ToolProfile: "MINIMAL"}}
	h := &WebhookHandler{db: x.f.rig.db, resolver: resolver, orch: runner, container: container, logger: newTestLogger()}
	return x, d, h, runner, container, a
}

func TestCollaborationAgentRuntimeRequiresSeparateEnvironmentBeforeProvider(t *testing.T) {
	x, d, h, runner, container, a := collaborationRuntimeFixture(t)
	rt, err := newCollaborationAgentTaskRuntime(d, h, nil, func(context.Context, collaborationAgentConfirmed) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	err = rt.Run(t.Context(), a, func() { t.Fatal("unqualified process confirmed") })
	if !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) || container.starts != 0 || runner.request != nil {
		t.Fatal("unqualified environment reached provider")
	}
	if rt.Classify(a, nil) != dispatch.OutcomeUnclear {
		t.Fatal("process return invented sealed completion")
	}
	x.clock.advance(x.store.ttl)
}

func TestCollaborationAgentRuntimeActualConfirmedSQLPublishesOriginalMandate(t *testing.T) {
	x, d, h, runner, container, a := collaborationRuntimeFixture(t)
	var captured collaborationAgentConfirmed
	rt, err := newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(ctx context.Context, b collaborationAgentConfirmed) error {
		// A different SQL connection sees the confirmed attempt before publish.
		var state, phase string
		if err := x.f.rig.db.QueryRowContext(ctx, `SELECT w.state,at.runtime_phase FROM work_items w JOIN work_attempts at ON at.work_id=w.id WHERE at.run_id=?`, a.RunID).Scan(&state, &phase); err != nil || state != "running" || phase != "confirmed" {
			return errors.New("published before SQL confirmation commit")
		}
		captured = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	started := func() {
		if err := work.NewStore(x.f.rig.db).StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, rt.Locator(a)); err != nil {
			t.Error(err)
		}
	}
	if err = rt.Run(t.Context(), a, started); !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) {
		t.Fatalf("exit without sealed completion: %v", err)
	}
	if container.starts != 1 || runner.request == nil || captured.mandate == "" || captured.runID != a.RunID || captured.workID != a.Item.ID || captured.attempt != a.Attempt || captured.generation != a.Generation {
		t.Fatal("actual attempt was not attached")
	}
	if captured.location.RunID != a.RunID || captured.location.ContainerID != "synthetic-private-container" || captured.location.AgentSlug != "principal-agent" || captured.execID != "synthetic-exec" {
		t.Fatal("provider identity not bound in host callback")
	}
	if !runner.request.SuppressSessionStream || !runner.request.SkipConvHistory || runner.request.MemoryEnabled {
		t.Fatal("private runtime output configuration absent")
	}
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(x.f.pages.artifactMasterToken, x.f.a.WorkspaceID, x.f.a.CrewID), x.f.a.WorkspaceID, x.f.a.AgentID, a.RunID)
	req := httptest.NewRequest(http.MethodPost, "/unused", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	ctx := collaborationIngressTestContext(t, x.fence, req.Context())
	ctx = context.WithValue(ctx, ctxInternalTokenWS, x.f.a.WorkspaceID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, x.f.a.CrewID)
	x.tx(t, func(tx *sql.Tx) {
		proof, e := x.store.Check(req.WithContext(ctx), tx, captured.mandate, x.key, "write")
		if e != nil || proof.runID != a.RunID {
			t.Fatalf("actual committed mandate unusable: %v", e)
		}
	})
}

func TestCollaborationAgentRuntimeFailedConfirmationNeverPublishes(t *testing.T) {
	x, d, h, runner, _, a := collaborationRuntimeFixture(t)
	published := false
	rt, err := newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(context.Context, collaborationAgentConfirmed) error { published = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	// started() has no returned error in the generic dispatch API. Its failure
	// must be discovered from canonical SQL rather than presumed successful.
	err = rt.Run(t.Context(), a, func() {})
	if !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) || published || runner.stopCalls != 1 {
		t.Fatal("unconfirmed SQL tuple published or stop omitted")
	}
	var state string
	if err = x.f.rig.db.QueryRow(`SELECT state FROM work_items WHERE id=?`, a.Item.ID).Scan(&state); err != nil || state != "starting" {
		t.Fatal("failed confirmation became terminal", state, err)
	}
}

func TestCollaborationAgentRuntimeWrongSQLLocatorNeverPublishes(t *testing.T) {
	x, d, h, _, _, a := collaborationRuntimeFixture(t)
	published := false
	rt, err := newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(context.Context, collaborationAgentConfirmed) error { published = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	err = rt.Run(t.Context(), a, func() {
		if e := work.NewStore(x.f.rig.db).StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, "agent-run:wrong-provider-location"); e != nil {
			t.Fatal(e)
		}
	})
	if !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) || published {
		t.Fatal("SQL locator different from actual provider accepted/published")
	}
}

func TestCollaborationAgentRuntimeSourceRevocationABABeforeConfirmationNeverRecaptures(t *testing.T) {
	x, d, h, runner, _, a := collaborationRuntimeFixture(t)
	published := false
	rt, err := newCollaborationAgentTaskRuntime(d, h, collaborationRuntimeEnvironment{}, func(context.Context, collaborationAgentConfirmed) error { published = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	runner.beforeStarted = func() {
		mustExec(t, x.f.rig.db, `UPDATE user_sessions SET revoked_at='2026-10-24T00:00:00Z' WHERE id=?`, x.f.receipt.SourceCredentialID)
		mustExec(t, x.f.rig.db, `UPDATE user_sessions SET revoked_at=NULL WHERE id=?`, x.f.receipt.SourceCredentialID)
	}
	err = rt.Run(t.Context(), a, func() {
		if e := work.NewStore(x.f.rig.db).StartRunning(t.Context(), a.Item.ID, a.RunID, a.Generation, rt.Locator(a)); e != nil {
			t.Fatal(e)
		}
	})
	if !errors.Is(err, orchestrator.ErrPrivateRunUnqualified) || published || runner.stopCalls != 1 {
		t.Fatal("restored original credential created a fresh mandate")
	}
	var captured, current string
	if err = x.f.rig.db.QueryRow(`SELECT p.user_authority_revision,CAST(u.authority_revision AS TEXT) FROM artifact_run_principals p JOIN users u ON u.id=p.human_user_id WHERE p.run_id=?`, a.RunID).Scan(&captured, &current); err != nil || captured != x.f.receipt.UserAuthorityRevision || captured == current {
		t.Fatalf("original immutable user fence refreshed: %s/%s %v", captured, current, err)
	}
}
