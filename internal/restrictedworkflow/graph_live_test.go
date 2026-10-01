//go:build linux && restrictedruntime_live

package restrictedworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
	"github.com/crewship-ai/crewship/internal/work"
)

// This acceptance path uses the production worker image and real durable broker
// accounting. The only upstream is a synthetic TLS server; no paid request runs.
func TestLiveNestedGraphProductionTextWorker(t *testing.T) {
	liveProductionGraph(t, "")
}

func TestLiveNestedGraphMixedProductionWorkers(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_NATIVE_IMAGE")
	if image == "" {
		t.Fatal("explicit mixed acceptance requires an owned native production image")
	}
	liveProductionGraph(t, image)
}

type liveMixedGraphExecutor struct {
	text   *restricteddispatch.TextRunner
	native *restricteddispatch.NativeRunner
}

// This observes only fixed error classes, never prompts, handles or secrets.
type liveObservedGraphAuthority struct {
	restricteddispatch.Authority
	mu       sync.Mutex
	failures map[string]int
}

func (a *liveObservedGraphAuthority) Resolve(ctx context.Context, handle string) (restrictedruntime.Plan, error) {
	plan, err := a.Authority.Resolve(ctx, handle)
	category := ""
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		category = "renewal_context_expired"
	} else if errors.Is(err, access.ErrDenied) {
		category = "authority_denied"
	} else if err != nil {
		category = "authority_storage_or_runtime_error"
	}
	if category != "" {
		a.mu.Lock()
		a.failures[category]++
		a.mu.Unlock()
	}
	return plan, err
}

func (e liveMixedGraphExecutor) ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error {
	return access.ErrDenied
}
func (e liveMixedGraphExecutor) SupportsWorkflowProfile(profile string) bool {
	return e.text.SupportsWorkflowProfile(profile) || e.native.SupportsWorkflowProfile(profile)
}
func (e liveMixedGraphExecutor) ExecuteWorkflowRun(ctx context.Context, request restricteddispatch.DelegatedRunRequest, emit func(string, string) error) (restricteddispatch.RunProof, error) {
	if request.Agent == "other" {
		return e.native.ExecuteWorkflowRun(ctx, request, emit)
	}
	if request.Agent != "agent" {
		return restricteddispatch.RunProof{}, access.ErrDenied
	}
	return e.text.ExecuteWorkflowRun(ctx, request, emit)
}

func TestLiveGraphFailedClientReleasesSharedAgent(t *testing.T) {
	liveProductionGraph(t, "", true)
}

type liveGraphExecutor interface {
	Executor
	proofExecutor
	SupportsWorkflowProfile(string) bool
}
type liveStoppedGraph struct {
	liveGraphExecutor
	db       *sql.DB
	managers []*restrictedruntime.Manager
}

func (e liveStoppedGraph) ConfirmWorkflowStopped(ctx context.Context, id string) error {
	rows, err := e.db.QueryContext(ctx, `SELECT attempt_id FROM restricted_workflow_attempt_roots WHERE workflow_id=?`, id)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		for _, manager := range e.managers {
			if err := manager.ConfirmAttemptStopped(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}
func liveProductionGraph(t *testing.T, nativeImage string, failFirst ...bool) {
	failingH1 := len(failFirst) > 0 && failFirst[0]
	image := os.Getenv("CREWSHIP_RESTRICTED_PRODUCTION_IMAGE")
	if image == "" {
		t.Fatal("explicit restrictedruntime_live acceptance requires an owned production worker image")
	}
	s, runner := graphFixture(t)
	if nativeImage != "" {
		if _, err := s.db.ExecContext(t.Context(), `UPDATE agents SET restricted_execution_profile='native_api_key' WHERE id='other'`); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`UPDATE agents SET llm_model='gpt-5-mini' WHERE id IN ('agent','other')`,
		`INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('graph-live-cap','w','workspace','w','month',100,'hard')`,
	} {
		if _, err := s.db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	calls := map[string]int{}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "gpt-5-mini" {
			t.Error("invalid frozen request")
			w.WriteHeader(403)
			return
		}
		encoded, _ := json.Marshal(body)
		input := string(encoded)
		user := "h1"
		if strings.Contains(input, "h2_PRIVATE_CANARY") || strings.Contains(input, "answer-h2") {
			user = "h2"
		}
		other := "h2"
		if user == "h2" {
			other = "h1"
		}
		if strings.Contains(input, other+"_PRIVATE_CANARY") || strings.Contains(input, "answer-"+other) {
			t.Error("foreign context reached real worker")
		}
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch key {
		case "synthetic-workflow-key", "synthetic-independent-workflow-key":
		default:
			t.Error("unapproved payer")
		}
		if key == "synthetic-independent-workflow-key" && !strings.Contains(input, "answer-"+user) {
			t.Error("B missing classified parent output")
		}
		var pending int
		if err := s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); err != nil || pending < 1 {
			t.Error("upstream preceded durable reservation")
		}
		mu.Lock()
		calls[user+":"+key]++
		mu.Unlock()
		if failingH1 && user == "h1" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if nativeImage != "" && key == "synthetic-independent-workflow-key" {
			items, ok := body["input"].([]any)
			if !ok {
				t.Error("native input was not frozen structured input")
				w.WriteHeader(400)
				return
			}
			followup := false
			for _, item := range items {
				value, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if value["type"] == "function_call_output" {
					followup = true
					output, _ := value["output"].(string)
					if value["call_id"] != "call_"+user || !strings.Contains(output, "OWN_"+user) || !strings.Contains(output, "code 0") {
						t.Error("native scratch result lost actor provenance")
					}
				}
				if value["type"] == "reasoning" && value["encrypted_content"] != "OPAQUE_"+user {
					t.Error("foreign native opaque state")
				}
			}
			var output []any
			if !followup {
				args, _ := json.Marshal(map[string]any{"cmd": "printf OWN_" + user + " > graph-canary.txt; cat graph-canary.txt", "shell": "/bin/sh", "login": false, "max_output_tokens": 100, "yield_time_ms": 1000})
				output = []any{map[string]any{"type": "reasoning", "id": "rs_" + user, "summary": []any{map[string]any{"type": "summary_text", "text": "synthetic reasoning"}}, "encrypted_content": "OPAQUE_" + user}, map[string]any{"type": "function_call", "id": "fc_" + user, "call_id": "call_" + user, "name": "exec_command", "arguments": string(args), "status": "completed"}}
			} else {
				output = []any{map[string]any{"type": "message", "id": "msg_" + user, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "answer-" + user, "annotations": []any{}}}}}
			}
			payload, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"id": "response_" + user + fmt.Sprint(followup), "model": "gpt-5-mini", "status": "completed", "output": output, "usage": map[string]any{"input_tokens": 20, "output_tokens": 5, "total_tokens": 25, "input_tokens_details": map[string]any{"cached_tokens": 0}}}})
			fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", payload)
			return
		}
		fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", "answer-"+user)
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-5-mini\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5,\"total_tokens\":25,\"input_tokens_details\":{\"cached_tokens\":0}}}}\n\n")
	}))
	defer upstream.Close()
	manager, err := restrictedruntime.New(filepath.Join(t.TempDir(), "runtime"), restrictedruntime.Docker{Image: image}, runner.Authority, runner.Authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err = manager.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = restrictedruntime.InstallAcceptanceTLS(manager, upstream); err != nil {
		t.Fatal(err)
	}
	runner.StartSession = nil
	runner.Manager = manager
	managers := []*restrictedruntime.Manager{manager}
	var nativeSessions []*restrictedruntime.Session
	observed := &liveObservedGraphAuthority{Authority: runner.Authority, failures: map[string]int{}}
	if nativeImage != "" {
		nativeManager, e := restrictedruntime.NewNative(filepath.Join(t.TempDir(), "native-runtime"), restrictedruntime.Docker{Image: nativeImage}, observed, runner.Authority, restrictedruntime.NativeLimits())
		if e != nil {
			t.Fatal(e)
		}
		defer nativeManager.Close()
		if e = nativeManager.Reconcile(t.Context()); e != nil {
			t.Fatal(e)
		}
		if e = restrictedruntime.InstallAcceptanceTLS(nativeManager, upstream); e != nil {
			t.Fatal(e)
		}
		nativeRunner := &restricteddispatch.NativeRunner{Authority: runner.Authority, Manager: nativeManager, MaxOutputTokens: 128}
		nativeRunner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
			session, err := nativeManager.Start(ctx, handle)
			if err == nil {
				nativeSessions = append(nativeSessions, session)
			}
			return session, err
		}
		managers = append(managers, nativeManager)
		s.executor = liveMixedGraphExecutor{runner, nativeRunner}
	}
	s.executor = liveStoppedGraph{s.executor.(liveGraphExecutor), s.db, managers}
	receipts := map[string]Receipt{}
	for _, user := range []string{"h1", "h2"} {
		receipts[user], err = s.AdmitManual(t.Context(), user, "w", "private-work", map[string]any{"task": user + "_PRIVATE_CANARY"}, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if failingH1 && user == "h1" {
			worked, err := s.DispatchNext(t.Context())
			item, getErr := s.ledger.Get(t.Context(), receipts[user].ID)
			if !worked || err == nil || getErr != nil || item.State != work.StateFailed {
				t.Fatalf("stopped H1 failure retained slot: %v %v %+v %v", worked, err, item, getErr)
			}
			continue
		}
		if worked, e := s.DispatchNext(t.Context()); !worked || e != nil {
			observed.mu.Lock()
			t.Log("native authority error classes", observed.failures)
			observed.mu.Unlock()
			for _, session := range nativeSessions {
				record := session.Record()
				t.Logf("native runtime status=%s reason=%s", record.Status, record.Reason)
			}
			t.Fatal("real graph failed", worked, e)
		}
		result, e := s.Result(t.Context(), user, "w", receipts[user].ID)
		if e != nil || result.State != "completed" || result.Outputs["return"] != "answer-"+user {
			t.Fatalf("missing own real result: %+v %v", result, e)
		}
	}
	if failingH1 {
		mu.Lock()
		defer mu.Unlock()
		if calls["h1:synthetic-workflow-key"] != 1 || calls["h2:synthetic-workflow-key"] != 2 || calls["h2:synthetic-independent-workflow-key"] != 1 {
			t.Fatalf("failed H1 replayed or blocked H2: %v", calls)
		}
		t.Log("H1 real worker upstream failure was physically confirmed stopped; H2 completed A/B/A on the shared agent without replaying H1")
		return
	}
	mu.Lock()
	bCalls, expectedCosts := 1, 6
	if nativeImage != "" {
		bCalls, expectedCosts = 2, 8
	}
	for _, user := range []string{"h1", "h2"} {
		if calls[user+":synthetic-workflow-key"] != 2 || calls[user+":synthetic-independent-workflow-key"] != bCalls {
			t.Error("independent provider slots not used", calls)
		}
	}
	mu.Unlock()
	var settled int
	if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_cost_reservations WHERE state='known'`).Scan(&settled); err != nil || settled != expectedCosts {
		t.Fatal("leaf costs not settled", settled, err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE access_attempts SET revoked_at='2026-09-30T13:00:00Z' WHERE principal_id='h1' AND agent_id='other' AND completed_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Result(t.Context(), "h1", "w", receipts["h1"].ID); err == nil {
		t.Fatal("revoked B output remained readable")
	}
	if _, err = s.Result(t.Context(), "h2", "w", receipts["h2"].ID); err != nil {
		t.Fatal("H1 source revoke affected H2", err)
	}
	if nativeImage != "" {
		queued, e := s.AdmitManual(t.Context(), "h2", "w", "private-work", map[string]any{"task": "h2_PRIVATE_CANARY"}, "", 0)
		if e != nil {
			t.Fatal(e)
		}
		var original string
		if e = s.db.QueryRowContext(t.Context(), `SELECT encrypted_value FROM credentials WHERE id='other-key'`).Scan(&original); e != nil {
			t.Fatal(e)
		}
		for _, value := range []string{"synthetic-rotated-cipher", original} {
			if _, e = s.db.ExecContext(t.Context(), `UPDATE credentials SET encrypted_value=? WHERE id='other-key'`, value); e != nil {
				t.Fatal(e)
			}
		}
		if worked, e := s.DispatchNext(t.Context()); !worked || e == nil {
			t.Fatal("revoked queued graph was accepted", worked, e)
		}
		if _, e = s.Result(t.Context(), "h2", "w", queued.ID); e == nil {
			t.Fatal("restored native provider revived the queued origin")
		}
		mu.Lock()
		defer mu.Unlock()
		if calls["h2:synthetic-workflow-key"] != 2 || calls["h2:synthetic-independent-workflow-key"] != 2 {
			t.Fatal("revoked native slot allowed an earlier paid text leaf")
		}
	}
}
