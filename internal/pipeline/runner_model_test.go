package pipeline

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
)

func TestRoutineAgentModelKeepsAdapterBoundModel(t *testing.T) {
	for _, tc := range []struct {
		name, adapter, model, tierAdapter, tierModel, want string
		explicit, wantErr                                  bool
	}{
		{name: "default Claude tier on Codex", adapter: "CODEX_CLI", model: "gpt-6.1-sol", tierAdapter: "claude", tierModel: "claude-sonnet-4-6", want: "gpt-6.1-sol"},
		{name: "same adapter tier", adapter: "CODEX_CLI", model: "gpt-6.1-sol", tierAdapter: "codex", tierModel: "gpt-6-mini", want: "gpt-6-mini"},
		{name: "full adapter key", adapter: "CODEX_CLI", model: "gpt-6.1-sol", tierAdapter: "CODEX_CLI", tierModel: "gpt-6-mini", want: "gpt-6-mini"},
		{name: "explicit cross adapter", adapter: "CODEX_CLI", model: "gpt-6.1-sol", tierAdapter: "claude", tierModel: "claude-sonnet-4-6", explicit: true, wantErr: true},
		{name: "explicit matching adapter", adapter: "CODEX_CLI", model: "gpt-6.1-sol", tierAdapter: "codex", tierModel: "gpt-6-mini", explicit: true, want: "gpt-6-mini"},
		{name: "Claude economic tier preserved", adapter: "CLAUDE_CODE", model: "claude-sonnet-5-5", tierAdapter: "claude", tierModel: "claude-haiku-5-5", want: "claude-haiku-5-5"},
		{name: "missing agent model", adapter: "CODEX_CLI", tierAdapter: "claude", tierModel: "claude-sonnet-4-6", wantErr: true},
		{name: "legacy direct caller", adapter: "CODEX_CLI", model: "gpt-6.1-sol", tierModel: "gpt-6-mini", want: "gpt-6-mini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := routineAgentModel(&chatbridge.ChatInfo{CLIAdapter: tc.adapter, LLMModel: tc.model}, AgentStepRequest{Adapter: tc.tierAdapter, Model: tc.tierModel, ModelOverride: func() string {
				if tc.explicit {
					return tc.tierAdapter + ":" + tc.tierModel
				}
				return ""
			}()})
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q/%v, want %q err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestRoutineCodexDefaultTierReachesRealAdapterCommand(t *testing.T) {
	container := &orchCovContainer{preflightSucceeds: true, agentStream: `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}` + "\n"}
	info := covChatInfo()
	info.CLIAdapter = "CODEX_CLI"
	info.LLMModel = "gpt-6.1-sol"
	resolver := &orchCovResolver{info: info}
	r := newOrchRunnerRig(t, container, resolver)
	res, err := r.RunStep(context.Background(), AgentStepRequest{WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent", Adapter: "claude", Model: "claude-sonnet-4-6", Prompt: "Do the task", PipelineID: "routine-model", StepID: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if res.EffectiveModel != "gpt-6.1-sol" || res.EffectiveAdapter != "CODEX_CLI" {
		t.Fatalf("effective identity not returned: %+v", res)
	}
	var b strings.Builder
	for _, s := range container.execScripts {
		b.WriteString(s)
		for _, part := range strings.Split(s, "'") {
			if raw, err := base64.StdEncoding.DecodeString(part); err == nil {
				b.Write(raw)
			}
		}
	}
	if !strings.Contains(b.String(), "gpt-6.1-sol") || strings.Contains(b.String(), "claude-sonnet-4-6") {
		t.Fatal("cross-provider tier leaked into Codex invocation")
	}
}

func TestRoutineExplicitCrossAdapterRefusedBeforeProvisioning(t *testing.T) {
	container := &orchCovContainer{}
	info := covChatInfo()
	info.CLIAdapter = "CODEX_CLI"
	info.LLMModel = "gpt-6.1-sol"
	resolver := &orchCovResolver{info: info}
	r := newOrchRunnerRig(t, container, resolver)
	for _, pin := range []string{"claude:claude-sonnet-4-6", "claude-sonnet-4-6", "codex:claude-sonnet-4-6"} {
		parsed := splitAdapterModel(pin, "claude")
		_, err := r.RunStep(t.Context(), AgentStepRequest{WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent", Adapter: parsed.Adapter, Model: parsed.Model, ModelOverride: pin, Prompt: "Do the task"})
		if err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("pin %q: expected clear mismatch, got %v", pin, err)
		}
		if len(container.execScripts) != 0 || len(resolver.createChatCalls) != 0 {
			t.Fatalf("pin %q produced runtime/chat side effects", pin)
		}
	}
}

func TestRoutineBareModelPinUsesConfiguredAgentAdapter(t *testing.T) {
	got, err := routineAgentModel(&chatbridge.ChatInfo{CLIAdapter: "CODEX_CLI", LLMModel: "gpt-default"}, AgentStepRequest{Adapter: "claude", Model: "gpt-6.1-sol", ModelOverride: "gpt-6.1-sol"})
	if err != nil || got != "gpt-6.1-sol" {
		t.Fatalf("bare model pin lost agent adapter intent: %q/%v", got, err)
	}
}

type effectiveModelTestRunner struct{}

func (effectiveModelTestRunner) RunStep(context.Context, AgentStepRequest) (AgentStepResult, error) {
	return AgentStepResult{Output: "verified", EffectiveModel: "gpt-6.1-sol", EffectiveAdapter: "CODEX_CLI"}, nil
}
func TestRecordedRoutineModelReflectsEffectiveAgentModel(t *testing.T) {
	_, db := openRunsTestDB(t)
	defer db.Close()
	installExecutionSchema(t, db)
	if _, err := db.Exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at) VALUES('effective-model-run','ws_runs','pln_a','test','running','2026-11-18T06:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	store := NewExecutionStore(db)
	ctx, parent, err := store.start(context.Background(), "effective-model-run", "work", "agent_run", "worker", "claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{runner: effectiveModelTestRunner{}, executionStore: store}
	if _, err := e.runRecordedAgent(ctx, AgentStepRequest{PipelineRunID: "effective-model-run", StepID: "work", AgentSlug: "worker", Model: "claude-sonnet-4-6"}); err != nil {
		t.Fatal(err)
	}
	if err := store.finish(ctx, parent, "verified", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT kind,model FROM pipeline_step_executions WHERE run_id='effective-model-run'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var kind, model string
		if err := rows.Scan(&kind, &model); err != nil {
			t.Fatal(err)
		}
		n++
		if model != "gpt-6.1-sol" {
			t.Fatalf("%s recorded requested tier instead of effective model: %s", kind, model)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("got%dexecutionrows,want parent+agent", n)
	}
}

func TestRoutineMalformedQualifiedPinFailsBeforeDefaultFallback(t *testing.T) {
	for _, pin := range []string{"claude:", ":gpt-6.1-sol", " :gpt-6.1-sol", "codex: ", ":"} {
		t.Run(pin, func(t *testing.T) {
			container := &orchCovContainer{}
			info := covChatInfo()
			info.CLIAdapter = "CODEX_CLI"
			info.LLMModel = "gpt-6.1-sol"
			resolver := &orchCovResolver{info: info}
			r := newOrchRunnerRig(t, container, resolver)
			parsed := splitAdapterModel(pin, "claude")
			_, err := r.RunStep(context.Background(), AgentStepRequest{WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent", Adapter: parsed.Adapter, Model: parsed.Model, ModelOverride: pin, Prompt: "Do the task"})
			if err == nil || !strings.Contains(err.Error(), "both adapter and model") {
				t.Fatalf("expected malformed pin error, got %v", err)
			}
			if len(container.execScripts) != 0 || len(resolver.createChatCalls) != 0 {
				t.Fatal("malformed pin caused runtime/chat side effects")
			}
		})
	}
}
