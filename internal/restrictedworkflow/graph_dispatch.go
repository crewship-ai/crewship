package restrictedworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
)

type sealedLeaf struct {
	Agent  string `json:"agent"`
	Cipher string `json:"cipher"`
}

func proofText(ctx context.Context, store access.Store, j job, agent string, proof restricteddispatch.RunProof) (string, error) {
	entries, err := proof.ReadWorkflow(ctx, store, j.Principal, j.Workspace, agent, j.Chat, j.ID)
	if err != nil || len(entries) != 1 || len(entries[0].Content) > 32768 {
		return "", ErrDenied
	}
	return entries[0].Content, nil
}

func (s *Service) readGraphOutputs(ctx context.Context, j job) (map[string]string, error) {
	g, err := decodeGraph(j)
	if err != nil {
		return nil, err
	}
	var sealed map[string]sealedLeaf
	if len(j.Proofs) > 256<<10 || json.Unmarshal([]byte(j.Proofs), &sealed) != nil || len(sealed) == 0 || len(sealed) > 16 {
		return nil, ErrDenied
	}
	used := 0
	store := access.Store{DB: s.db}
	var read func(*graphRecipe, string) (map[string]string, error)
	read = func(r *graphRecipe, path string) (map[string]string, error) {
		outputs := map[string]string{}
		for index, node := range r.Steps {
			key := path + strconv.Itoa(index)
			if node.Child != nil {
				child, err := read(node.Child, key+"/")
				if err != nil {
					return nil, err
				}
				raw, err := json.Marshal(child)
				if err != nil || len(raw) > 128<<10 {
					return nil, ErrDenied
				}
				outputs[node.ID] = string(raw)
				continue
			}
			record, ok := sealed[key]
			if !ok || record.Agent != node.Agent {
				return nil, ErrDenied
			}
			used++
			proof, err := restricteddispatch.OpenRunProof(record.Cipher)
			if err != nil {
				return nil, ErrDenied
			}
			outputs[node.ID], err = proofText(ctx, store, j, node.Agent, proof)
			if err != nil {
				return nil, err
			}
		}
		return outputs, nil
	}
	outputs, err := read(g.Root, "")
	if err != nil || used != len(sealed) {
		return nil, ErrDenied
	}
	raw, err := json.Marshal(outputs)
	if err != nil || len(raw) > 256<<10 || string(raw) != j.Outputs {
		return nil, ErrDenied
	}
	return outputs, nil
}

func (s *Service) executeGraph(ctx context.Context, j job, originHandle string, assignment dispatch.Assignment) (map[string]string, string, error) {
	g, err := decodeGraph(j)
	if err != nil {
		return nil, "", err
	}
	executor, ok := s.executor.(proofExecutor)
	if !ok {
		return nil, "", ErrUnsupported
	}
	store := access.Store{DB: s.db}
	var rights []access.Right
	if json.Unmarshal([]byte(j.Rights), &rights) != nil {
		return nil, "", ErrDenied
	}
	parentHandle, parent, err := store.Admit(ctx, j.Principal, j.Workspace, j.Agent, j.Chat, "", rights)
	if err != nil {
		return nil, "", err
	}
	complete := false
	defer func() {
		if !complete {
			clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = store.RevokeAttempt(clean, parentHandle)
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if err = s.checkOwnership(ctx, tx, assignment); err != nil {
		return nil, "", err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO restricted_workflow_attempt_roots(access_attempt_id,workflow_id,run_id,generation) VALUES(?,?,?,?)`, parent.ID, j.ID, assignment.RunID, assignment.Generation); err != nil {
		return nil, "", err
	}
	if err = tx.Commit(); err != nil {
		return nil, "", err
	}
	if _, err = store.BuildContext(ctx, parent, "Execute the immutable admitted workflow."); err != nil {
		return nil, "", err
	}
	var seedID string
	if s.db.QueryRowContext(ctx, `SELECT id FROM access_context WHERE attempt_id=? AND kind='history' AND role='user' LIMIT 1`, j.Origin).Scan(&seedID) != nil {
		return nil, "", ErrDenied
	}
	bound := map[string]bool{}
	var bind func(*graphRecipe) error
	bind = func(r *graphRecipe) error {
		for _, node := range r.Steps {
			if node.Child != nil {
				if err := bind(node.Child); err != nil {
					return err
				}
				continue
			}
			if bound[node.Agent] {
				continue
			}
			bound[node.Agent] = true
			if err := s.providers.BindProviderDelegation(ctx, parentHandle, j.ID, node.Agent, node.ProviderHash, 4096); err != nil {
				return err
			}
		}
		return nil
	}
	if err = bind(g.Root); err != nil {
		return nil, "", err
	}
	proofs := map[string]restricteddispatch.RunProof{}
	sealed := map[string]sealedLeaf{}
	check := func() error {
		if ctx.Err() != nil || s.checkJob(ctx, s.db, j, originHandle) != nil || s.checkOwnership(ctx, s.db, assignment) != nil {
			return ErrDenied
		}
		if _, err := store.Resolve(ctx, parentHandle); err != nil {
			return ErrDenied
		}
		for path, proof := range proofs {
			if _, err := proofText(ctx, store, j, sealed[path].Agent, proof); err != nil {
				return ErrDenied
			}
		}
		return nil
	}
	var inputs map[string]any
	if json.Unmarshal([]byte(j.Inputs), &inputs) != nil {
		return nil, "", ErrDenied
	}
	totalText := 0
	var run func(context.Context, *graphRecipe, string, map[string]any, []string) (map[string]string, []string, error)
	run = func(runCtx context.Context, r *graphRecipe, path string, supplied map[string]any, inherited []string) (map[string]string, []string, error) {
		d, err := compileTyped(r.Raw, true)
		if err != nil || len(d.DSL.Steps) != len(r.Steps) {
			return nil, nil, ErrDenied
		}
		localInputs, err := normalizeInputs(d, supplied)
		if err != nil {
			return nil, nil, err
		}
		outputs := map[string]string{}
		sources := map[string][]string{}
		allSources := []string{}
		for index, step := range d.DSL.Steps {
			if check() != nil {
				return nil, nil, ErrDenied
			}
			node := r.Steps[index]
			if node.ID != step.ID {
				return nil, nil, ErrDenied
			}
			key := path + strconv.Itoa(index)
			selected := append([]string{seedID}, inherited...)
			templates := []string{step.Prompt}
			for _, value := range step.NestedInputs {
				if text, ok := value.(string); ok {
					templates = append(templates, text)
				}
			}
			for _, template := range templates {
				for _, match := range refs.FindAllStringSubmatch(template, -1) {
					parts := strings.Split(strings.TrimSpace(match[1]), ".")
					if len(parts) == 3 && parts[0] == "steps" {
						selected = append(selected, sources[parts[1]]...)
					}
				}
			}
			selected = uniqueSources(selected)
			if len(selected) > 64 {
				return nil, nil, ErrDenied
			}
			stepCtx := runCtx
			stop := func() {}
			if step.TimeoutSec > 0 || node.Child == nil {
				timeout := 5 * time.Minute
				if step.TimeoutSec > 0 {
					timeout = time.Duration(step.TimeoutSec) * time.Second
				}
				stepCtx, stop = context.WithTimeout(runCtx, timeout)
			}
			if node.Child != nil {
				childInputs := map[string]any{}
				for name, value := range step.NestedInputs {
					childInputs[name] = renderInput(value, localInputs, outputs)
				}
				child, ids, err := run(stepCtx, node.Child, key+"/", childInputs, selected)
				stop()
				if err != nil {
					return nil, nil, err
				}
				raw, err := json.Marshal(child)
				if err != nil || len(raw) > 128<<10 {
					return nil, nil, ErrDenied
				}
				outputs[step.ID] = string(raw)
				sources[step.ID] = ids
				allSources = append(allSources, ids...)
				continue
			}
			prompt := pipeline.Render(step.Prompt, pipeline.RenderContext{Inputs: localInputs, StepOutputs: outputs})
			if len(prompt) == 0 || len(prompt) > 32768 {
				stop()
				return nil, nil, ErrDenied
			}
			var text strings.Builder
			done := false
			emit := func(kind, value string) error {
				if stepCtx.Err() != nil || check() != nil {
					return ErrDenied
				}
				switch kind {
				case "text":
					if done || text.Len()+len(value) > 32768 {
						return ErrDenied
					}
					text.WriteString(value)
				case "done":
					if done || value != "" {
						return ErrDenied
					}
					done = true
				default:
					return ErrDenied
				}
				return nil
			}
			proof, err := executor.ExecuteWorkflowRun(stepCtx, restricteddispatch.DelegatedRunRequest{User: j.Principal, Workspace: j.Workspace, Agent: node.Agent, Chat: j.Chat, ParentHandle: parentHandle, Input: prompt, Rights: rights, SourceEntryIDs: selected}, emit)
			stop()
			if errors.Is(err, context.Canceled) {
				return nil, nil, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, nil, context.DeadlineExceeded
			}
			if err != nil || !done {
				return nil, nil, ErrDenied
			}
			actual, err := proofText(ctx, store, j, node.Agent, proof)
			if err != nil || actual != text.String() {
				return nil, nil, ErrDenied
			}
			totalText += len(actual)
			if totalText > 128<<10 {
				return nil, nil, ErrDenied
			}
			cipher, err := proof.Seal()
			if err != nil {
				return nil, nil, ErrDenied
			}
			proofs[key] = proof
			sealed[key] = sealedLeaf{node.Agent, cipher}
			outputs[step.ID] = actual
			sources[step.ID] = proof.ContextIDs()
			allSources = append(allSources, proof.ContextIDs()...)
		}
		return outputs, uniqueSources(allSources), nil
	}
	outputs, _, err := run(ctx, g.Root, "", inputs, []string{seedID})
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return nil, "", context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, "", context.DeadlineExceeded
	}
	if err != nil || check() != nil {
		return nil, "", ErrDenied
	}
	if err = store.CompleteAttempt(ctx, parentHandle); err != nil {
		return nil, "", err
	}
	if store.CheckContextAttempt(ctx, parentHandle) != nil {
		return nil, "", ErrDenied
	}
	for path, proof := range proofs {
		if _, err := proofText(ctx, store, j, sealed[path].Agent, proof); err != nil {
			return nil, "", ErrDenied
		}
	}
	raw, err := json.Marshal(sealed)
	if err != nil || len(raw) > 256<<10 {
		return nil, "", ErrDenied
	}
	complete = true
	return outputs, string(raw), nil
}

func uniqueSources(ids []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	return result
}

func renderInput(value any, inputs map[string]any, outputs map[string]string) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	match := refs.FindStringSubmatch(text)
	if len(match) == 2 && match[0] == text {
		parts := strings.Split(strings.TrimSpace(match[1]), ".")
		if len(parts) == 2 && parts[0] == "inputs" {
			return inputs[parts[1]]
		}
	}
	return pipeline.Render(text, pipeline.RenderContext{Inputs: inputs, StepOutputs: outputs})
}
