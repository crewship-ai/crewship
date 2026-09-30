//go:build linux

package restricteddispatch

import (
	"context"

	"github.com/crewship-ai/crewship/internal/access"
)

// BoundBuildNativePrompt imports host-classified provenance before freezing a
// delegated prompt. The handle is host-only and never a worker request field.
type BoundBuildNativePrompt func(context.Context, string, access.Attempt) (NativePrompt, error)

// PrepareDelegatedNative requires an immutable host workflow provider slot.
// Ordinary PrepareNative keeps its existing provider inheritance rules.
func (a Authority) PrepareDelegatedNative(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, limit int64, build BoundBuildNativePrompt) (string, access.Attempt, error) {
	if parent == "" || build == nil || limit < 1 || limit > 32768 {
		return "", access.Attempt{}, access.ErrDenied
	}
	return a.prepareDelegatedBound(ctx, user, workspace, agent, chat, parent, rights, func(ctx context.Context, handle string, child access.Attempt) ([]string, error) {
		if child.AdmissionOperation != "run" {
			return nil, access.ErrDenied
		}
		if err := a.pinDelegatedProvider(ctx, child, limit, "native_api_key"); err != nil {
			return nil, err
		}
		if err := a.bindNativeProjectInputs(ctx, child, nil); err != nil {
			return nil, err
		}
		prompt, err := build(ctx, handle, child)
		if err != nil {
			return nil, err
		}
		return a.freezeNativePrompt(ctx, child, prompt)
	})
}

// SupportsWorkflowProfile advertises only the installed bounded native adapter.
func (r *NativeRunner) SupportsWorkflowProfile(profile string) bool {
	return r != nil && profile == "native_api_key" && r.Authority.Store.DB != nil &&
		(r.Manager != nil || r.StartSession != nil) && r.MaxOutputTokens >= 1 && r.MaxOutputTokens <= 32768
}

// ExecuteWorkflowRun returns the exact completed output proof to trusted host
// orchestration. Source rights and provider slots are revalidated on admission.
func (r *NativeRunner) ExecuteWorkflowRun(ctx context.Context, request DelegatedRunRequest, emit func(string, string) error) (RunProof, error) {
	if r == nil || r.Authority.Store.DB == nil || (r.Manager == nil && r.StartSession == nil) || emit == nil || len(request.Input) == 0 || len(request.Input) > 32768 || (request.ParentHandle == "" && len(request.SourceEntryIDs) != 0) {
		return RunProof{}, access.ErrDenied
	}
	store := r.Authority.Store
	var chatAgent string
	if store.DB.QueryRowContext(ctx, `SELECT agent_id FROM chats WHERE id=? AND workspace_id=? AND visibility='private'`, request.Chat, request.Workspace).Scan(&chatAgent) != nil || (request.ParentHandle == "" && chatAgent != request.Agent) {
		return RunProof{}, access.ErrDenied
	}
	build := func(ctx context.Context, attempt access.Attempt) (NativePrompt, error) {
		prompt, err := store.BuildContext(ctx, attempt, request.Input)
		if err != nil {
			return NativePrompt{}, err
		}
		return NativePrompt{Instructions: prompt.System, Input: prompt.Input}, nil
	}
	var handle string
	var attempt access.Attempt
	var err error
	if request.ParentHandle == "" {
		handle, attempt, err = r.Authority.PrepareNative(ctx, request.User, request.Workspace, request.Agent, request.Chat, "", request.Rights, r.MaxOutputTokens, func(ctx context.Context, attempt access.Attempt) (NativePrompt, error) {
			if err := r.Authority.bindNativeProjectInputs(ctx, attempt, nil); err != nil {
				return NativePrompt{}, err
			}
			return build(ctx, attempt)
		})
	} else {
		if len(request.SourceEntryIDs) == 0 || len(request.SourceEntryIDs) > 64 {
			return RunProof{}, access.ErrDenied
		}
		handle, attempt, err = r.Authority.PrepareDelegatedNative(ctx, request.User, request.Workspace, request.Agent, request.Chat, request.ParentHandle, request.Rights, r.MaxOutputTokens, func(ctx context.Context, handle string, attempt access.Attempt) (NativePrompt, error) {
			importContext := store.ImportDelegatedContext
			if attempt.WorkflowContinuation {
				importContext = store.ImportWorkflowContinuation
			}
			if _, err := importContext(ctx, request.ParentHandle, handle, request.SourceEntryIDs); err != nil {
				return NativePrompt{}, err
			}
			return build(ctx, attempt)
		})
	}
	if err != nil {
		return RunProof{}, err
	}
	return r.executeNativePrepared(ctx, request.User, request.Workspace, request.Chat, request.Input, handle, attempt, emit, true)
}

// ExecuteDelegatedRun is the error-only host compatibility wrapper.
func (r *NativeRunner) ExecuteDelegatedRun(ctx context.Context, request DelegatedRunRequest, emit func(string, string) error) error {
	if request.ParentHandle == "" {
		return access.ErrDenied
	}
	_, err := r.ExecuteWorkflowRun(ctx, request, emit)
	return err
}
