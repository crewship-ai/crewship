package restrictedworkflow

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
)

const nestedRecipe = `{"dsl_version":"1.0","name":"nested","inputs":[{"name":"context","type":"string","required":true}],"steps":[{"id":"review","type":"agent_run","agent_slug":"other","prompt":"Review {{ inputs.context }}"}]}`
const graphRecipeJSON = `{"dsl_version":"1.0","name":"graph","inputs":[{"name":"task","type":"string","required":true}],"steps":[{"id":"first","type":"agent_run","agent_slug":"worker","prompt":"{{ inputs.task }}"},{"id":"nested","type":"call_pipeline","pipeline_slug":"nested-work","inputs":{"context":"{{ steps.first.output }}"}},{"id":"return","type":"agent_run","agent_slug":"worker","prompt":"Return {{ steps.nested.output }}"}]}`

func graphFixture(t *testing.T) (*Service, *restricteddispatch.TextRunner) {
	s, runner := fixture(t)
	exec := func(query string, args ...any) {
		if _, err := s.db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	cipher, err := encryption.Encrypt("synthetic-independent-workflow-key")
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role,llm_provider,llm_model,restricted_execution_profile) VALUES('other','w','crew','Other','other','AGENT','OPENAI','fixture-model','responses_text')`)
	exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,created_by) VALUES('other-key','w','Other',?,'API_KEY','OPENAI','owner')`, cipher)
	exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('other-grant','other','other-key','OPENAI_API_KEY')`)
	exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,author_crew_id,status) VALUES('nested','w','nested-work','Nested',?,?,'crew','active')`, nestedRecipe, hash(nestedRecipe))
	exec(`UPDATE pipelines SET definition_json=?,definition_hash=? WHERE id='routine'`, graphRecipeJSON, hash(graphRecipeJSON))
	store := access.Store{DB: s.db}
	for _, user := range []string{"h1", "h2"} {
		m, err := store.Membership(t.Context(), user, "w")
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Replace(t.Context(), "owner", user, "w", "restricted", m, []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}, {Kind: "agent", ID: "other", Operation: "run"}, {Kind: "agent", ID: "other", Operation: "delegate"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, runner
}

func TestNestedDelegatedWorkflowTwoHumansAndReturn(t *testing.T) {
	s, runner := graphFixture(t)
	base := runner.StartSession
	counts := map[string]map[string]int{}
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		a, err := runner.Authority.Store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		if counts[a.Principal] == nil {
			counts[a.Principal] = map[string]int{}
		}
		counts[a.Principal][a.Agent]++
		var launch string
		if s.db.QueryRowContext(ctx, `SELECT command_json FROM restricted_launches WHERE attempt_id=?`, a.ID).Scan(&launch) != nil {
			t.Fatal("missing frozen leaf")
		}
		other := "h1"
		if a.Principal == "h1" {
			other = "h2"
		}
		if strings.Contains(launch, other+"_PRIVATE_CANARY") || strings.Contains(launch, "answer-"+other) {
			t.Fatal("foreign graph context entered leaf")
		}
		if a.Agent == "other" && !strings.Contains(launch, "answer-"+a.Principal) {
			t.Fatal("parent output provenance absent from nested B input")
		}
		if a.Agent == "agent" && counts[a.Principal][a.Agent] == 2 && !a.WorkflowContinuation {
			t.Fatal("return did not use original frozen A continuation")
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
	for range 2 {
		worked, err := s.DispatchNext(t.Context())
		if !worked || err != nil {
			t.Fatal("graph failed", worked, err)
		}
	}
	for _, user := range []string{"h1", "h2"} {
		result, err := s.Result(t.Context(), user, "w", jobs[user].ID)
		if err != nil || result.State != "completed" || result.Outputs["return"] != "answer-"+user || !strings.Contains(result.Outputs["nested"], "answer-"+user) {
			t.Fatalf("missing nested private result: %+v %v", result, err)
		}
		if counts[user]["agent"] != 2 || counts[user]["other"] != 1 {
			t.Fatal("unexpected leaf execution", counts)
		}
		other := "h1"
		if user == "h1" {
			other = "h2"
		}
		if _, err = s.Result(t.Context(), other, "w", jobs[user].ID); err == nil {
			t.Fatal("foreign actor read nested receipt")
		}
	}
	var child string
	if s.db.QueryRowContext(t.Context(), `SELECT a.id FROM access_attempts a JOIN chats c ON c.id=a.chat_id WHERE a.agent_id='other' AND a.principal_id='h1' AND a.completed_at IS NOT NULL ORDER BY a.generation LIMIT 1`).Scan(&child) != nil {
		t.Fatal("missing completed B leaf")
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE access_attempts SET revoked_at='2026-09-30T13:00:00Z' WHERE id=?`, child); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Result(t.Context(), "h1", "w", jobs["h1"].ID); err == nil {
		t.Fatal("completed B source revocation did not hide graph output")
	}
	if _, err := s.Result(t.Context(), "h2", "w", jobs["h2"].ID); err != nil {
		t.Fatal("H1 revoke affected H2 graph", err)
	}
}

func TestGraphMutationBehindQueueNeverLaunches(t *testing.T) {
	for name, query := range map[string]string{"nested declaration": `UPDATE pipelines SET definition_json='{}' WHERE id='nested'`, "target key rotate restore": `UPDATE credentials SET encrypted_value='changed' WHERE id='other-key'; UPDATE credentials SET encrypted_value=(SELECT value FROM saved_key) WHERE id='other-key'`, "target profile": `UPDATE agents SET restricted_execution_profile='disabled' WHERE id='other'`} {
		t.Run(name, func(t *testing.T) {
			s, runner := graphFixture(t)
			if _, err := s.db.ExecContext(t.Context(), `CREATE TEMP TABLE saved_key AS SELECT encrypted_value AS value FROM credentials WHERE id='other-key'`); err != nil {
				t.Fatal(err)
			}
			receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "classified"}, "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			runner.StartSession = func(context.Context, string) (restricteddispatch.TextSession, error) {
				t.Fatal("stale graph launched")
				return nil, nil
			}
			if worked, err := s.DispatchNext(t.Context()); !worked || err == nil {
				t.Fatal("stale queued graph accepted")
			}
			if _, err = s.Result(t.Context(), "h1", "w", receipt.ID); err == nil {
				t.Fatal("stale graph result readable")
			}
		})
	}
}

func TestGraphMissingDelegateCannotEscalate(t *testing.T) {
	s, _ := graphFixture(t)
	store := access.Store{DB: s.db}
	m, err := store.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []access.Right{{Kind: "agent", ID: "agent", Operation: "run"}, {Kind: "agent", ID: "other", Operation: "run"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "x"}, "", 0); err == nil {
		t.Fatal("nested target run substituted for delegate")
	}
}

func TestNestedInputSourceRevocationStopsTargetAndReturn(t *testing.T) {
	s, runner := graphFixture(t)
	base := runner.StartSession
	var starts int
	runner.StartSession = func(ctx context.Context, handle string) (restricteddispatch.TextSession, error) {
		a, err := runner.Authority.Store.Resolve(ctx, handle)
		if err != nil {
			return nil, err
		}
		starts++
		if a.Agent == "other" {
			var first string
			if err = s.db.QueryRowContext(ctx, `SELECT id FROM access_attempts WHERE principal_id='h1' AND agent_id='agent' AND completed_at IS NOT NULL AND parent_id IS NOT NULL ORDER BY generation LIMIT 1`).Scan(&first); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.ExecContext(ctx, `UPDATE access_attempts SET revoked_at='2026-09-30T13:00:00Z' WHERE id=?`, first); err != nil {
				t.Fatal(err)
			}
			if _, err = runner.Authority.Store.Resolve(ctx, handle); err == nil {
				t.Fatal("nested input lost source provenance")
			}
		}
		return base(ctx, handle)
	}
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "classified-source"}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := s.DispatchNext(t.Context()); !worked || err == nil {
		t.Fatal("revoked nested source completed graph", worked, err)
	}
	if starts != 2 {
		t.Fatal("return A launched after source revoke", starts)
	}
	if _, err = s.Result(t.Context(), "h1", "w", receipt.ID); err == nil {
		t.Fatal("revoked graph result leaked")
	}
}

func TestNestedGraphRejectsCyclesAndUnsupportedSteps(t *testing.T) {
	for name, raw := range map[string]string{
		"cycle":  `{"dsl_version":"1.0","name":"cycle","steps":[{"id":"again","type":"call_pipeline","pipeline_slug":"private-work"}]}`,
		"script": `{"dsl_version":"1.0","name":"script","steps":[{"id":"shell","type":"script","command":"echo secret"}]}`,
		"http":   `{"dsl_version":"1.0","name":"http","steps":[{"id":"network","type":"http","url":"https://example.test"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := graphFixture(t)
			if _, err := s.db.ExecContext(t.Context(), `UPDATE pipelines SET definition_json=?,definition_hash=? WHERE id='nested'`, raw, hash(raw)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "secret"}, "", 0); err == nil {
				t.Fatal("unsupported nested graph admitted")
			}
			var n int
			if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM restricted_workflow_jobs`).Scan(&n); err != nil || n != 0 {
				t.Fatal("unsupported graph reached durable queue", n, err)
			}
		})
	}
}
