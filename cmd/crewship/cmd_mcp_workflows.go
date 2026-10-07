package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/crewship-ai/crewship/internal/cli"
)

const mcpRoutineRunOperation = "get_api_v1_workspaces_workspaceId_pipeline-runs_runId"

const mcpRestrictedRunOperation = "get_api_v1_workspaces_workspaceId_restricted-routine-runs_runId"

func routineReadOperation(restricted bool) string {
	if restricted {
		return mcpRestrictedRunOperation
	}
	return mcpRoutineRunOperation
}

type mcpRunInput struct {
	Restricted bool   `json:"restricted,omitempty" yaml:"restricted,omitempty" jsonschema:"True only for a receipt marked restricted; uses the actor-scoped endpoint"`
	RunID      string `json:"run_id" yaml:"run_id" jsonschema:"Durable routine run ID from the start receipt"`
}
type mcpWaitInput struct {
	Restricted  bool   `json:"restricted,omitempty" yaml:"restricted,omitempty" jsonschema:"Carry restricted=true from a private receipt when resuming observation"`
	RunID       string `json:"run_id" yaml:"run_id" jsonschema:"Durable routine run ID; this tool never starts a run"`
	WaitSeconds int    `json:"wait_seconds,omitempty" yaml:"wait_seconds,omitempty" jsonschema:"Maximum wait in seconds, default 30, maximum 300"`
}
type mcpRoutineListInput struct {
	Restricted bool `json:"restricted,omitempty" yaml:"restricted,omitempty" jsonschema:"Use the authorized private routine projection for restricted members"`
}

type mcpCrewInput struct {
	CrewID string `json:"crew_id" yaml:"crew_id" jsonschema:"Exact crew ID from crewship_list_crews"`
}
type mcpRoutineStartInput struct {
	Slug                   string          `json:"slug" yaml:"slug" jsonschema:"Routine slug from crewship_list_routines"`
	Inputs                 json.RawMessage `json:"inputs,omitempty" yaml:"inputs,omitempty" jsonschema:"Routine input object; inspect the routine definition first"`
	ExpectedExecutionHash  string          `json:"expected_execution_hash,omitempty" yaml:"expected_execution_hash,omitempty" jsonschema:"Optional expected execution graph hash from restricted routine discovery"`
	ExpectedDefinitionHash string          `json:"expected_definition_hash,omitempty" yaml:"expected_definition_hash,omitempty" jsonschema:"Optional expected definition hash to refuse changed routines"`
	IdempotencyKey         string          `json:"idempotency_key,omitempty" yaml:"idempotency_key,omitempty" jsonschema:"Caller-chosen key for server deduplication; retain the same key when reconciling uncertain submission"`
	ConfirmWrite           bool            `json:"confirm_write,omitempty" yaml:"confirm_write,omitempty" jsonschema:"Model acknowledgment; operator write policy and any required human approval still apply"`
	DryRun                 bool            `json:"dry_run,omitempty" yaml:"dry_run,omitempty" jsonschema:"Validate request metadata without starting the routine"`
	WaitSeconds            int             `json:"wait_seconds,omitempty" yaml:"wait_seconds,omitempty" jsonschema:"Optional bounded wait after accepted run, 0 returns receipt immediately, maximum 300"`
}

func (s *cliMCP) workflowRead(ctx context.Context, id string, params map[string]string) (any, error) {
	op, err := s.operation(id)
	if err != nil {
		return nil, err
	}
	if op.RequiresYes {
		return nil, apiValidation("workflow read resolved to a mutation")
	}
	return s.request(ctx, mcpRequestInput{OperationID: id, PathParams: params})
}

func mcpResponseBody(result any) (map[string]any, error) {
	envelope, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unexpected API response envelope")
	}
	body, ok := envelope["body"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object response")
	}
	return body, nil
}

func (s *cliMCP) waitRoutine(ctx context.Context, in mcpWaitInput, progress func(*cli.PipelineRunDetail)) (any, error) {
	if in.WaitSeconds < 0 || in.WaitSeconds > 300 {
		return nil, apiValidation("wait_seconds must be between 0 and 300")
	}
	if in.WaitSeconds == 0 {
		in.WaitSeconds = 30
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(in.WaitSeconds)*time.Second)
	defer cancel()
	var last any
	read := func(ctx context.Context, id string) (*cli.PipelineRunDetail, error) {
		value, err := s.workflowRead(ctx, routineReadOperation(in.Restricted), map[string]string{"runId": id})
		if err != nil {
			return nil, err
		}
		body, err := mcpResponseBody(value)
		if err != nil {
			return nil, err
		}
		status, _ := body["status"].(string)
		if status == "" {
			return nil, fmt.Errorf("routine response is missing status")
		}
		step, _ := body["current_step"].(string)
		last = body // Preserve JSON numbers and server extensions, not a lossy typed copy.
		return &cli.PipelineRunDetail{ID: id, Status: status, CurrentStep: step}, nil
	}
	detail, err := cli.PollPipelineRunWith(ctx, in.RunID, 2*time.Second, read, progress)
	result := map[string]any{"restricted": in.Restricted, "run_id": in.RunID, "run": last, "terminal": detail != nil && detail.IsTerminal(), "timed_out": errors.Is(err, context.DeadlineExceeded), "cancelled": errors.Is(err, context.Canceled)}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		result["resume_tool"] = "crewship_run_wait"
		result["resume_arguments"] = mcpWaitInput{RunID: in.RunID, Restricted: in.Restricted, WaitSeconds: in.WaitSeconds}
		return result, nil
	}
	return result, err
}

func mcpWorkflowProgress(ctx context.Context, req *mcp.CallToolRequest) func(*cli.PipelineRunDetail) {
	ticks := 0
	return func(detail *cli.PipelineRunDetail) {
		ticks++
		if req.Session == nil || req.Params.GetProgressToken() == nil {
			return
		}
		// Status only: progress must not duplicate potentially sensitive outputs.
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: req.Params.GetProgressToken(), Progress: float64(ticks), Message: "Routine status: " + detail.Status})
	}
}

func (s *cliMCP) addWorkflowTools(server *mcp.Server, approvals *mcpApprovalGate) error {
	reads := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	for _, spec := range []struct{ name, description, operation string }{
		{"crewship_list_agents", "List visible agents in the pinned workspace", "get_api_v1_agents"},
		{"crewship_list_crews", "List visible crews in the pinned workspace", "get_api_v1_crews"},
	} {
		mcp.AddTool(server, &mcp.Tool{Name: spec.name, Description: spec.description, Annotations: reads}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			value, err := s.workflowRead(ctx, spec.operation, nil)
			return cliMCPResult(value, err)
		})
	}
	mcp.AddTool(server, &mcp.Tool{Name: "crewship_list_routines", Description: "List routine definitions, or the authorized private projection for restricted members", Annotations: reads}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRoutineListInput) (*mcp.CallToolResult, any, error) {
		operation := "get_api_v1_workspaces_workspaceId_pipelines"
		if in.Restricted {
			operation = "get_api_v1_workspaces_workspaceId_restricted-routines"
		}
		value, err := s.workflowRead(ctx, operation, nil)
		return cliMCPResult(value, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "crewship_run_status", Description: "Read a routine run's current status, errors and recorded output", Annotations: reads}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRunInput) (*mcp.CallToolResult, any, error) {
		value, err := s.workflowRead(ctx, routineReadOperation(in.Restricted), map[string]string{"runId": in.RunID})
		return cliMCPResult(value, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "crewship_run_wait", Description: "Wait for an existing routine run with bounded polling and progress notifications. Timeout preserves the receipt and never restarts or cancels the run.", Annotations: reads}, func(ctx context.Context, req *mcp.CallToolRequest, in mcpWaitInput) (*mcp.CallToolResult, any, error) {
		value, err := s.waitRoutine(ctx, in, mcpWorkflowProgress(ctx, req))
		return cliMCPResult(value, err)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "crewship_run_diagnose", Description: "Read a routine run and its bounded logs together; reports unavailable logs separately", Annotations: reads}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpRunInput) (*mcp.CallToolResult, any, error) {
		params := map[string]string{"runId": in.RunID}
		run, err := s.workflowRead(ctx, routineReadOperation(in.Restricted), params)
		if err != nil {
			return cliMCPResult(nil, err)
		}
		if in.Restricted {
			return cliMCPResult(map[string]any{"run": run, "logs_available": false}, nil)
		}
		logs, err := s.workflowRead(ctx, mcpRoutineRunOperation+"_logs", params)
		result := map[string]any{"run": run, "logs": logs}
		if err != nil {
			result["logs_error"] = cli.NewErrorEnvelope(err)
		}
		return cliMCPResult(result, nil)
	})
	mcp.AddTool(server, &mcp.Tool{Name: "crewship_crew_status", Description: "Read crew configuration and runtime status together; never starts or changes a container", Annotations: reads}, func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCrewInput) (*mcp.CallToolResult, any, error) {
		params := map[string]string{"crewId": in.CrewID}
		crew, err := s.workflowRead(ctx, "get_api_v1_crews_crewId", params)
		if err != nil {
			return cliMCPResult(nil, err)
		}
		runtime, err := s.workflowRead(ctx, "get_api_v1_crews_crewId_container-status", params)
		result := map[string]any{"crew": crew, "runtime": runtime}
		if err != nil {
			result["runtime_error"] = cli.NewErrorEnvelope(err)
		}
		return cliMCPResult(result, nil)
	})
	schema, err := jsonschema.For[mcpRoutineStartInput](&jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[json.RawMessage](): {}}})
	if err != nil {
		return err
	}
	destructive := true
	server.AddTool(&mcp.Tool{Name: "crewship_routine_start", Description: "Start a routine asynchronously with the same write policy and human approval as crewship_write; optionally wait up to 300 seconds. Returns durable receipts. Deferred receipts without a run_id are returned unchanged; do not invent a run ID or resubmit.", InputSchema: schema, Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := s.startRoutine(ctx, req, approvals)
		return result, err
	})
	return nil
}

func (s *cliMCP) startRoutine(ctx context.Context, req *mcp.CallToolRequest, approvals *mcpApprovalGate) (*mcp.CallToolResult, error) {
	finish := func(v any, err error) (*mcp.CallToolResult, error) { r, _, e := cliMCPResult(v, err); return r, e }
	var in mcpRoutineStartInput
	dec := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return finish(nil, apiValidation("invalid routine arguments"))
	}
	if in.WaitSeconds < 0 || in.WaitSeconds > 300 {
		return finish(nil, apiValidation("wait_seconds must be between 0 and 300"))
	}
	if len(in.Inputs) > apiInputLimit {
		return finish(nil, apiValidation("routine inputs exceed 10 MiB"))
	}
	if len(in.Inputs) > 0 && (!json.Valid(in.Inputs) || !strings.HasPrefix(strings.TrimSpace(string(in.Inputs)), "{")) {
		return finish(nil, apiValidation("inputs must be a JSON object"))
	}
	body := map[string]any{"inputs": json.RawMessage(`{}`)}
	if len(in.Inputs) > 0 {
		body["inputs"] = in.Inputs
	}
	if in.ExpectedDefinitionHash != "" {
		body["expected_definition_hash"] = in.ExpectedDefinitionHash
	}
	if in.ExpectedExecutionHash != "" {
		body["expected_execution_hash"] = in.ExpectedExecutionHash
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return finish(nil, err)
	}
	input := mcpRequestInput{OperationID: "post_api_v1_workspaces_workspaceId_pipelines_slug_run", PathParams: map[string]string{"slug": in.Slug}, Body: encoded, Headers: []string{"Prefer=respond-async"}, ConfirmWrite: in.ConfirmWrite, DryRun: in.DryRun, IdempotencyKey: in.IdempotencyKey}
	op, err := s.operation(input.OperationID)
	if err != nil {
		return finish(nil, err)
	}
	requester := s
	if !in.DryRun {
		if !s.writeAllowed(op) || !in.ConfirmWrite {
			return finish(nil, apiValidation("write policy or model acknowledgment missing"))
		}
		if s.requireApproval {
			var revision [32]byte
			requester, revision, err = s.approvalSnapshot()
			if err != nil {
				return finish(nil, err)
			}
			pending, err := approvals.approve(req, input, revision)
			if err != nil {
				return finish(nil, err)
			}
			if pending != nil {
				return pending, nil
			}
		}
	}
	receipt, err := requester.request(ctx, input)
	if err != nil {
		return finish(nil, err)
	}
	if in.DryRun || in.WaitSeconds == 0 {
		return finish(receipt, nil)
	}
	returned, err := mcpResponseBody(receipt)
	if err != nil {
		return finish(map[string]any{"receipt": receipt, "wait_error": "receipt is not an object; inspect it without resubmitting"}, nil)
	}
	runID, _ := returned["run_id"].(string)
	if runID == "" {
		return finish(map[string]any{"receipt": receipt, "wait_available": false}, nil)
	}
	waited, err := s.waitRoutine(ctx, mcpWaitInput{RunID: runID, Restricted: returned["restricted"] == true, WaitSeconds: in.WaitSeconds}, mcpWorkflowProgress(ctx, req))
	result := map[string]any{"receipt": receipt, "wait": waited}
	if err != nil {
		result["wait_error"] = cli.NewErrorEnvelope(err)
	}
	return finish(result, nil)
}
