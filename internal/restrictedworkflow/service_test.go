//go:build linux

package restrictedworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
)

const routine = `{"dsl_version":"1.0","name":"private-work","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"first","type":"agent_run","agent_slug":"worker","prompt":"{{ inputs.task }}"},{"id":"second","type":"agent_run","agent_slug":"worker","prompt":"Continue {{ steps.first.output }}"}]}`

type session struct {
	text string
	done chan struct{}
}

func (s session) Output(context.Context) (string, error) { return s.text, nil }
func (s session) Done() <-chan struct{}                  { return s.done }
func (s session) Stop(string)                            {}
func fixture(t *testing.T) (*Service, *restricteddispatch.TextRunner) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("31", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "v1")
	db := testutil.MigratedSQLDB(t)
	exec := func(q string, args ...any) {
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO users(id,email) VALUES('owner','owner@workflow.test'),('h1','h1@workflow.test'),('h2','h2@workflow.test')`)
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','workflow-w')`)
	exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,capabilities) VALUES('mo','w','owner','OWNER','[]'),('m1','w','h1','MEMBER','["routine.run"]'),('m2','w','h2','MEMBER','["routine.run"]')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','w','Crew','workflow-crew')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,llm_provider,llm_model,restricted_execution_profile) VALUES('agent','w','crew','Worker','worker','AGENT','OPENAI','fixture-model','responses_text')`)
	cipher, err := encryption.Encrypt("synthetic-workflow-key")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('key','w','Key',?,'API_KEY','OPENAI','owner')`, cipher)
	exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('key-grant','agent','key','OPENAI_API_KEY')`)
	exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('routine','w','private-work','Private',?,?,'crew','active')`, routine, hash(routine))
	store := access.Store{DB: db}
	for _, user := range []string{"h1", "h2"} {
		m, err := store.Membership(t.Context(), user, "w")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Replace(t.Context(), "owner", user, "w", "restricted", m, []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}}); err != nil {
			t.Fatal(err)
		}
	}
	runner := &restricteddispatch.TextRunner{Authority: restricteddispatch.Authority{Store: store}, MaxOutputTokens: 128}
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		a, err := store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		done := make(chan struct{})
		close(done)
		return session{"{\"type\":\"text\",\"text\":\"answer-" + a.Principal + "\"}\n{\"type\":\"done\"}\n", done}, nil
	}
	service, err := New(db, runner)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service, runner
}
func TestPrivateDurableWorkflowTwoHumansAndRecovery(t *testing.T) {
	s, runner := fixture(t)
	starts := map[string]int{}
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		a, err := runner.Authority.Store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		starts[a.Principal]++
		var launch string
		if err = s.db.QueryRowContext(ctx, `SELECT command_json FROM restricted_launches WHERE attempt_id=?`, a.ID).Scan(&launch); err != nil {
			t.Fatal(err)
		}
		other := "h2"
		if a.Principal == "h2" {
			other = "h1"
		}
		if strings.Contains(launch, other+"_PRIVATE_CANARY") || strings.Contains(launch, "answer-"+other) {
			t.Fatal("foreign routine context entered builder")
		}
		if starts[a.Principal] == 2 && !strings.Contains(launch, "answer-"+a.Principal) {
			t.Fatal("same-scope prior step absent")
		}
		return base(ctx, handle)
	}
	jobs := map[string]Receipt{}
	for _, user := range []string{"h1", "h2"} {
		r, err := s.AdmitManual(t.Context(), user, "w", "private-work", map[string]any{"task": user + "_PRIVATE_CANARY"}, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		jobs[user] = r
	}
	recovered, err := New(s.db, runner)
	if err != nil {
		t.Fatal(err)
	}
	for range jobs {
		worked, err := recovered.DispatchNext(t.Context())
		if err != nil || !worked {
			t.Fatalf("dispatch %v %v", worked, err)
		}
	}
	for user, receipt := range jobs {
		result, err := recovered.Result(t.Context(), user, "w", receipt.ID)
		if err != nil || result.State != "completed" || len(result.Outputs) != 2 {
			t.Fatalf("result %+v %v", result, err)
		}
		other := "h1"
		if user == "h1" {
			other = "h2"
		}
		if _, err = recovered.Result(t.Context(), other, "w", receipt.ID); !errors.Is(err, ErrDenied) {
			t.Fatalf("foreign result %v", err)
		}
		list, err := recovered.ResultsForActor(t.Context(), user, "w")
		if err != nil || len(list) != 1 || list[0].ID != receipt.ID {
			t.Fatalf("foreign listing %+v %v", list, err)
		}
	}
	var shared int
	if err = s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pipeline_runs`).Scan(&shared); err != nil || shared != 0 {
		t.Fatalf("shared run outputs persisted %d %v", shared, err)
	}
}
func TestQueuedWorkflowOriginalPolicyCannotRevive(t *testing.T) {
	for _, change := range []string{"grants", "role", "capability", "recipe", "status", "profile"} {
		t.Run(change, func(t *testing.T) {
			s, runner := fixture(t)
			starts := 0
			runner.StartSession = func(context.Context, string) (restricteddispatch.TextSession, error) { starts++; return nil, ErrDenied }
			receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			exec := func(q string, args ...any) {
				if _, err = s.db.ExecContext(t.Context(), q, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "grants":
				store := runner.Authority.Store
				m, err := store.Membership(t.Context(), "h1", "w")
				if err != nil {
					t.Fatal(err)
				}
				m, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", m, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}}); err != nil {
					t.Fatal(err)
				}
			case "role":
				exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='m1'`)
				exec(`UPDATE workspace_members SET role='MEMBER' WHERE id='m1'`)
			case "capability":
				exec(`UPDATE workspace_members SET capabilities='[]' WHERE id='m1'`)
				exec(`UPDATE workspace_members SET capabilities='["routine.run"]' WHERE id='m1'`)
			case "recipe":
				exec(`UPDATE pipelines SET definition_json='{}' WHERE id='routine'`)
				exec(`UPDATE pipelines SET definition_json=? WHERE id='routine'`, routine)
			case "status":
				exec(`UPDATE pipelines SET status='disabled' WHERE id='routine'`)
				exec(`UPDATE pipelines SET status='active' WHERE id='routine'`)
			case "profile":
				exec(`UPDATE agents SET restricted_execution_profile='disabled' WHERE id='agent'`)
				exec(`UPDATE agents SET restricted_execution_profile='responses_text' WHERE id='agent'`)
			}
			worked, err := s.DispatchNext(t.Context())
			if !worked || !errors.Is(err, ErrDenied) || starts != 0 {
				t.Fatalf("revived queue worked=%v err=%v starts=%d", worked, err, starts)
			}
			if _, err = s.Result(t.Context(), "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
				t.Fatalf("old scope result revived %v", err)
			}
			var state string
			if err = s.db.QueryRowContext(t.Context(), `SELECT state FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&state); err != nil || state != "failed" {
				t.Fatalf("queue terminal %s %v", state, err)
			}
		})
	}
}
func TestWorkflowRequiresRoutinePermissionAndBoundedTypedDeclaration(t *testing.T) {
	s, _ := fixture(t)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE workspace_members SET capabilities='[]' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "run grant alone"}, "", 0); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent run bypassed routine gate %v", err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE workspace_members SET capabilities='["routine.run"]' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"dsl_version":"1.0","name":"private-work","steps":[{"id":"script","type":"script","script":{"path":"/crew/shared/steal.sh"}}]}`,
		`{"dsl_version":"1.0","name":"private-work","steps":[{"id":"nested","type":"call_pipeline","pipeline_slug":"trusted"}]}`,
		strings.Replace(routine, `"agent_slug":"worker","prompt":"Continue`, `"agent_slug":"foreign","prompt":"Continue`, 1),
		strings.Replace(routine, `"prompt":"{{ inputs.task }}"`, `"prompt":"{{ secrets.openai }}"`, 1),
		strings.Replace(routine, `"prompt":"{{ inputs.task }}"`, `"prompt":"{{ inputs.task }}","model_override":"expensive"`, 1),
	} {
		if _, err := s.db.ExecContext(t.Context(), `UPDATE pipelines SET definition_json=? WHERE id='routine'`, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", 0); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported declaration admitted %v", err)
		}
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_workflow_jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unsupported queue count=%d err=%v", count, err)
	}
}
func TestUnknownRunningWorkflowNeedsReconciliationWithoutRetry(t *testing.T) {
	s, runner := fixture(t)
	starts := 0
	base := runner.StartSession
	runner.StartSession = func(ctx context.Context, h string) (restricteddispatch.TextSession, error) {
		starts++
		return base(ctx, h)
	}
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(t.Context(), `UPDATE restricted_workflow_jobs SET state='running' WHERE id=?`, receipt.ID); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ledger.Claim(t.Context(), work.ClaimOptions{LeaseOwner: "old", Limits: work.SerialAgentLimits(), WorkID: receipt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ledger.MarkStarting(t.Context(), receipt.ID, claimed.RunID, claimed.Generation, "old-private-runtime"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE work_attempts SET lease_expires_at='2000-01-01T00:00:00Z' WHERE run_id=?`, claimed.RunID); err != nil {
		t.Fatal(err)
	}
	if err = s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRowContext(t.Context(), `SELECT state FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&state); err != nil || state != "running" || starts != 0 {
		t.Fatalf("unknown paid retry %s starts=%d %v", state, starts, err)
	}
	result, err := s.Result(t.Context(), "h1", "w", receipt.ID)
	if err != nil || result.State != "needs_reconciliation" || len(result.Outputs) != 0 {
		t.Fatalf("unknown outcome %+v %v", result, err)
	}
}
func TestWorkflowDeclarationRevocationStopsLiveConsumer(t *testing.T) {
	s, runner := fixture(t)
	base := runner.StartSession
	var live string
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		live = handle
		session, err := base(ctx, handle)
		if err == nil {
			_, err = s.db.ExecContext(ctx, `UPDATE pipelines SET status='disabled' WHERE id='routine'`)
		}
		return session, err
	}
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	worked, err := s.DispatchNext(t.Context())
	if !worked || !errors.Is(err, ErrDenied) || live == "" {
		t.Fatalf("live revoke %v %v handle=%v", worked, err, live != "")
	}
	if _, err = runner.Authority.Store.Resolve(t.Context(), live); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("declaration revocation did not stop consumer %v", err)
	}
	var raw string
	if err = s.db.QueryRowContext(t.Context(), `SELECT outputs_json FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("revoked output persisted %s %v", raw, err)
	}
}
func TestWorkflowQueueIdentityAndEncryptionFailClosed(t *testing.T) {
	s, _ := fixture(t)
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE restricted_workflow_jobs SET principal_id='h2' WHERE id=?`, `UPDATE restricted_workflow_jobs SET recipe_json='{}' WHERE id=?`, `UPDATE restricted_workflow_jobs SET origin_handle_ciphertext='tamper' WHERE id=?`} {
		if _, err = s.db.ExecContext(t.Context(), q, receipt.ID); err == nil {
			t.Fatal("durable authority changed")
		}
	}
	var handle string
	if err = s.db.QueryRowContext(t.Context(), `SELECT origin_handle_ciphertext FROM restricted_workflow_jobs WHERE id=?`, receipt.ID).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	if _, err = (access.Store{DB: s.db}).Resolve(t.Context(), receipt.ID); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("public job ID authorized %v", err)
	}
	if json.Valid([]byte(handle)) {
		t.Fatal("opaque origin stored as plaintext JSON")
	}
	t.Setenv("ENCRYPTION_KEY", "")
	if err = s.Start(t.Context()); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing host key started %v", err)
	}
	if _, err = s.Result(t.Context(), "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing host key returned output %v", err)
	}
}
