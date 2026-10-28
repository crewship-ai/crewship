package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

// These stand-ins exercise normal Work claim/start/confirmation, the actual
// internal CreateRun host helper and native publication. They are not a real
// provider/model or public/browser acceptance, and never seed a run row.
type nativeAgentRuntimeProvider struct{ collaborationRuntimeContainer }

func (*nativeAgentRuntimeProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil
}

type nativeAgentRuntimeRunner struct {
	manager      *craftNativeAgentManager
	err          error
	run          string
	withoutApply bool
	cancelHuman  context.Context
}

func (r *nativeAgentRuntimeRunner) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, handler orchestrator.EventHandler) error {
	r.run = req.RunID
	if handler != nil {
		return errors.New("private task installed a content handler")
	}
	if err := req.ExecGate(ctx); err != nil {
		return err
	}
	r.manager.mu.Lock()
	before := r.manager.runs[req.RunID]
	r.manager.mu.Unlock()
	if before != nil {
		return errors.New("native mandate published before provider confirmed")
	}
	req.OnExecStarted("unit-native-original-exec")
	r.manager.mu.Lock()
	entry := r.manager.runs[req.RunID]
	r.manager.mu.Unlock()
	if entry == nil {
		return errors.New("actual confirmed attempt was not published")
	}
	g := entry.grant
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.manager.human.pages.artifactMasterToken, g.Scope.WorkspaceID, g.CrewID), g.Scope.WorkspaceID, g.AgentID, req.RunID)
	call := func(body string) *httptest.ResponseRecorder {
		q := httptest.NewRequest(http.MethodPost, "/api/v1/internal/craft-native/task", strings.NewReader(body))
		q.Header.Set("Authorization", "Bearer "+token)
		bound := context.WithValue(context.WithValue(ctx, ctxInternalTokenWS, g.Scope.WorkspaceID), ctxInternalTokenCrew, g.CrewID)
		w := httptest.NewRecorder()
		r.manager.toolCall(w, q.WithContext(bound))
		return w
	}
	w := call(`{"operation":"acquire"}`)
	if w.Code != 200 {
		return fmt.Errorf("unit acquire %d %s", w.Code, w.Body)
	}
	if r.cancelHuman != nil {
		q := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@native-agent-page/craft/tasks/cancel", strings.NewReader("{}")).WithContext(r.cancelHuman)
		q.SetPathValue("slug", "@native-agent-page")
		w := httptest.NewRecorder()
		r.manager.cancelTask(w, q)
		if w.Code != 200 {
			return fmt.Errorf("unit genuine H1 stop %d %s", w.Code, w.Body)
		}
		return context.Canceled
	}
	if r.withoutApply {
		return nil
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &lease); err != nil {
		return err
	}
	w = call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"dispatched-unit-edit","commands":[{"command":"text.insert","params":{"text":"unit task"}}]}`, lease.Generation))
	if w.Code != 200 {
		return fmt.Errorf("unit apply %d %s", w.Code, w.Body)
	}
	w = call(fmt.Sprintf(`{"operation":"release","generation":%q}`, lease.Generation))
	if w.Code != 200 {
		return fmt.Errorf("unit release %d %s", w.Code, w.Body)
	}
	return r.err
}
func (*nativeAgentRuntimeRunner) StopRun(context.Context, string) (bool, error)    { return false, nil }
func (*nativeAgentRuntimeRunner) RunIsAlive(context.Context, string) (bool, error) { return false, nil }

type nativeAgentRuntimeResolver struct{ *collaborationRuntimeResolver }

func (r *nativeAgentRuntimeResolver) UpdateRun(ctx context.Context, id, status string, exit *int, failure *string, metadata map[string]interface{}) error {
	q := httptest.NewRequest(http.MethodPatch, "/api/v1/internal/runs/"+id, jsonBody(map[string]any{"status": status, "exit_code": exit, "error_message": failure, "metadata": metadata})).WithContext(ctx)
	q.SetPathValue("runId", id)
	w := httptest.NewRecorder()
	r.handler.UpdateRun(w, q)
	if w.Code != http.StatusOK {
		return fmt.Errorf("project run: HTTP %d %s", w.Code, w.Body)
	}
	return nil
}

func TestCraftNativeAgentNormalDispatcherPublishesOnlyConfirmedMandateAndQualifiedOutcome(t *testing.T) {
	for _, scenario := range []struct{ noReceipt, cancelled bool }{{false, false}, {true, false}, {false, true}} {
		noReceipt, cancelled := scenario.noReceipt, scenario.cancelled
		t.Run(fmt.Sprintf("noReceipt=%t/cancelled=%t", noReceipt, cancelled), func(t *testing.T) {
			f, m, human := nativeAgentAdmissionFixture(t)
			f.rig.db.SetMaxOpenConns(1)
			mustExec(t, f.rig.db, `PRAGMA synchronous=FULL`)
			f.pages.artifactMasterToken = "unit-native-dispatch-master"
			accepted, err := m.produceTask(human, "@native-agent-page", craftNativeAgentTaskRequest{f.a.ChatID, f.a.MessageID, true})
			if err != nil {
				t.Fatal(err)
			}
			e := m.entries[accepted.WorkID]
			cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
			m.documents.applications["wordcraft"] = craftNativeApplication{Store: cfg}
			m.config.Profiles = map[string]CraftNativeDocumentProfile{"wordcraft": {Application: "wordcraft"}}
			m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
				return []byte(`{"body":["unit dispatched edit"]}`), nil
			}
			m.config.AgentInspect = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{"text":"unit view"}`), nil
			}
			versions := artifacts.NewVersionStore(f.rig.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
			if _, err = versions.Apply(human, artifacts.VersionKey{WorkspaceID: f.rig.wsID, PageID: "native-agent-page"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"body":[]}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil }); err != nil {
				t.Fatal(err)
			}
			internal := NewInternalHandler(f.rig.db, "unit-native-dispatch-master", newTestLogger())
			wireTestJournalForHandler(t, f.rig.db, internal)
			resolver := &nativeAgentRuntimeResolver{&collaborationRuntimeResolver{handler: internal, original: e.grant.Original, message: f.a.MessageID, info: &chatbridge.ChatInfo{AgentID: f.a.AgentID, AgentSlug: "principal-agent", AgentRole: "AGENT", CrewID: f.a.CrewID, CrewSlug: "principal-crew", WorkspaceID: f.a.WorkspaceID, CLIAdapter: "CLAUDE_CODE", TimeoutSecs: 5, ToolProfile: "MINIMAL"}}}
			runner := &nativeAgentRuntimeRunner{manager: m, withoutApply: noReceipt}
			if cancelled {
				runner.cancelHuman = human
			}
			h := &WebhookHandler{db: f.rig.db, resolver: resolver, orch: runner, container: &nativeAgentRuntimeProvider{}, logger: newTestLogger()}
			rt := &craftNativeAgentRuntime{WebhookRuntime: NewWebhookRuntime(h), manager: m, environment: collaborationRuntimeEnvironment{}}
			d := dispatch.New(work.NewStore(f.rig.db), rt, m, dispatch.Config{Owner: "unit-native-dispatch", Kinds: []work.Kind{{Source: work.SourceChat, DomainKind: craftNativeAgentDomain}}, Limits: work.SerialAgentLimits()}, newTestLogger())
			m.runtime, m.dispatcher = rt, d
			worked, err := d.RunOne(t.Context())
			if err != nil || !worked {
				t.Fatal("normal dispatcher", worked, err)
			}
			item, err := work.NewStore(f.rig.db).Get(t.Context(), accepted.WorkID)
			wantState, wantStatus := work.StateSucceeded, "COMPLETED"
			if noReceipt {
				wantState, wantStatus = work.StateFailed, "FAILED"
			}
			if cancelled {
				wantState, wantStatus = work.StateCancelled, "CANCELLED"
			}
			if err != nil || item.State != wantState {
				t.Fatalf("unqualified outcome state=%v runner=%s err=%v", item, runner.run, err)
			}
			if !noReceipt && !cancelled {
				var actor, message string
				if err = f.rig.db.QueryRow(`SELECT actor,message FROM artifact_versions WHERE page_id='native-agent-page' AND version=2`).Scan(&actor, &message); err != nil || actor != "agent:"+f.a.AgentID || !strings.Contains(message, "native-agent:"+runner.run) {
					t.Fatal("persisted original agent/run", actor, message, err)
				}
			} else {
				var versions int
				if err := f.rig.db.QueryRow(`SELECT count(*) FROM artifact_versions WHERE page_id='native-agent-page'`).Scan(&versions); err != nil || versions != 1 {
					t.Fatal("no-receipt run invented a version", versions, err)
				}
			}
			projection, owned, err := work.NewStore(f.rig.db).RunProjection(t.Context(), runner.run)
			if err != nil || !owned || !projection.Ready || projection.Status != wantStatus {
				t.Fatal("native outcome missing durable projection", projection, owned, err)
			}
			if cancelled {
				if _, ok := projection.Result.Metadata["native_saved"]; ok {
					t.Fatal("cancelled output invented saved state", projection.Result.Metadata)
				}
				if projection.Result.Metadata["native_runtime_stopped"] != true {
					t.Fatal("physical stop proof absent", projection.Result.Metadata)
				}
			} else if projection.Result.Metadata["native_saved"] != !noReceipt || (!noReceipt && projection.Result.Metadata["native_version"] != "2") {
				t.Fatal("unqualified result metadata", projection.Result.Metadata)
			}
			// A foreign/unregistered outbox backlog must neither be projected by
			// this private runtime nor starve its own acknowledged outcome.
			if !noReceipt && !cancelled {
				tx, err := f.rig.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				for n := 1; n <= 100; n++ {
					_, err = tx.Exec(`INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,started_at,run_status,run_result_json) VALUES(?,?,?,?,?,'2000-01-01T00:00:00Z','2000-01-01T00:00:00Z','2000-01-01T00:00:00Z','COMPLETED','{}')`, fmt.Sprintf("unit-unregistered-%03d", n), accepted.WorkID, n+1, n+1, "unit-unregistered")
					if err != nil {
						tx.Rollback()
						t.Fatal(err)
					}
				}
				if _, err = tx.Exec(`UPDATE work_attempts SET run_projected_at=NULL WHERE run_id=?`, runner.run); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if err = rt.FlushRunOutcomes(t.Context()); err != nil {
					t.Fatal(err)
				}
				var foreignWrites int
				if err = f.rig.db.QueryRow(`SELECT count(*) FROM work_attempts WHERE run_id LIKE 'unit-unregistered-%' AND (run_projected_at IS NOT NULL OR run_projection_attempted_at IS NOT NULL)`).Scan(&foreignWrites); err != nil || foreignWrites != 0 {
					t.Fatal("native runtime wrote unrelated outcomes", foreignWrites, err)
				}
			}
			var projected, terminal int
			if err := f.rig.db.QueryRow(`SELECT count(*) FROM work_attempts WHERE run_id=? AND run_projected_at IS NOT NULL`, runner.run).Scan(&projected); err != nil || projected != 1 {
				t.Fatal("projection not acknowledged", projected, err)
			}
			if err := f.rig.db.QueryRow(`SELECT count(*) FROM journal_entries WHERE trace_id=? AND entry_type=?`, runner.run, func() string {
				if cancelled {
					return "run.cancelled"
				}
				if noReceipt {
					return "run.failed"
				}
				return "run.completed"
			}()).Scan(&terminal); err != nil || terminal != 1 {
				t.Fatal("canonical terminal absent", terminal, err)
			}

		})
	}
}
