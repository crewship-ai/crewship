package restricteddispatch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// NativeRunner owns the separate scoped Codex scratch-tool application path.
type NativeRunner struct {
	StartSession    func(context.Context, string) (TextSession, error)
	Authority       Authority
	Manager         *restrictedruntime.Manager
	MaxOutputTokens int64
}

func (r *NativeRunner) Execute(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, emit, false, nil, nil)
}

// ExecuteRun is selected only by the dedicated authenticated CLI/run route.
func (r *NativeRunner) ExecuteRun(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, emit, true, nil, nil)
}

// ExecuteRunWithRights is trusted host orchestration, never a client-supplied rights list.
func (r *NativeRunner) ExecuteRunWithRights(ctx context.Context, user, workspace, chat, input string, rights []access.Right, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, emit, true, rights, nil)
}
func (r *NativeRunner) ExecuteWithProjectFiles(ctx context.Context, user, workspace, chat, input string, ids []string, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, emit, false, nil, ids)
}
func (r *NativeRunner) ExecuteRunWithProjectFiles(ctx context.Context, user, workspace, chat, input string, ids []string, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, emit, true, nil, ids)
}
func (r *NativeRunner) execute(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error, runOperation bool, rights []access.Right, ids []string) error {
	if r == nil || (r.Manager == nil && r.StartSession == nil) || emit == nil || len(input) == 0 || len(input) > 32768 {
		return access.ErrDenied
	}
	store := r.Authority.Store
	var err error
	if !runOperation {
		allowed, err := chataudience.CanReadInWorkspace(ctx, store.DB, chat, user, workspace)
		if err != nil {
			return err
		}
		if !allowed {
			return access.ErrDenied
		}
	}
	var agent, profile string
	if err := store.DB.QueryRowContext(ctx, `SELECT c.agent_id,a.restricted_execution_profile FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id WHERE c.id=? AND c.workspace_id=? AND c.visibility='private'`, chat, workspace).Scan(&agent, &profile); err != nil {
		return access.ErrDenied
	}
	if profile != "native_api_key" {
		return access.ErrDenied
	}
	handle, attempt, err := r.Authority.prepareNativeProjectFiles(ctx, user, workspace, agent, chat, "", rights, r.MaxOutputTokens, ids, func(ctx context.Context, attempt access.Attempt) (NativePrompt, error) {
		binding, err := loadProvider(ctx, store.DB, attempt.ID)
		if err != nil || binding.Profile != "native_api_key" {
			return NativePrompt{}, access.ErrDenied
		}
		prompt, err := store.BuildContext(ctx, attempt, input)
		if err != nil {
			return NativePrompt{}, err
		}
		return NativePrompt{Instructions: prompt.System, Input: prompt.Input}, nil
	}, !runOperation)
	if err != nil {
		return err
	}
	_, err = r.executeNativePrepared(ctx, user, workspace, chat, input, handle, attempt, emit, runOperation)
	return err
}

// executeNativePrepared owns one already-admitted and host-frozen native run.
// Delegated callers must bind inputs/import provenance before freezing the prompt.
func (r *NativeRunner) executeNativePrepared(ctx context.Context, user, workspace, chat, input, handle string, attempt access.Attempt, emit func(string, string) error, runOperation bool) (RunProof, error) {
	if r == nil || (r.Manager == nil && r.StartSession == nil) || emit == nil || len(input) == 0 || len(input) > 32768 {
		return RunProof{}, access.ErrDenied
	}
	store := r.Authority.Store
	var err error
	complete := false
	defer func() {
		state := "failed"
		if complete {
			state = "completed"
		} else if ctx.Err() != nil {
			state = "canceled"
		}
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if store.RecordOutcome(clean, handle, state) != nil {
			slog.Error("record restricted attempt outcome failed")
		}
	}()
	defer func() {
		if complete {
			return
		}
		clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		_ = store.RevokeAttempt(clean, handle)
	}()
	current, err := store.Resolve(ctx, handle)
	if err != nil || current.ID != attempt.ID || current.Scope != attempt.Scope || current.Generation != attempt.Generation || current.Principal != user || current.Workspace != workspace || current.Chat != chat || current.Agent != attempt.Agent {
		return RunProof{}, access.ErrDenied
	}
	if _, err = store.AppendContext(ctx, handle, "user", input); err != nil {
		return RunProof{}, err
	}
	start := r.StartSession
	if start == nil {
		start = func(ctx context.Context, handle string) (TextSession, error) { return r.Manager.Start(ctx, handle) }
	}
	session, err := start(ctx, handle)
	if err != nil {
		return RunProof{}, err
	}
	defer session.Stop("application_finished")
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	offset := 0
	var text strings.Builder
	finished := false
	artifactBytes := 0
	artifacts := map[string]bool{}
	consume := func() error {
		raw, err := session.Output(ctx)
		if err != nil {
			return err
		}
		if len(raw) < offset {
			return access.ErrDenied
		}
		for {
			idx := strings.IndexByte(raw[offset:], '\n')
			if idx < 0 {
				break
			}
			line := raw[offset : offset+idx]
			offset += idx + 1
			var frame struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Name    string `json:"name"`
				Content []byte `json:"content"`
			}
			if json.Unmarshal([]byte(line), &frame) != nil {
				return errors.New("invalid restricted output")
			}
			switch frame.Type {
			case "text":
				if finished || text.Len()+len(frame.Text) > 32768 {
					return access.ErrDenied
				}
				// Recheck both immutable attempt and live chat audience for every event.
				if _, err = store.Resolve(ctx, handle); err != nil {
					return err
				}
				if !runOperation {
					ok, err := chataudience.CanReadInWorkspace(ctx, store.DB, chat, user, workspace)
					if err != nil {
						return err
					}
					if !ok {
						return access.ErrDenied
					}
				}
				if err = emit("text", frame.Text); err != nil {
					return err
				}
				text.WriteString(frame.Text)
			case "artifact":
				if finished || artifacts[frame.Name] || len(artifacts) >= 32 || len(frame.Content) > 1<<20 || artifactBytes+len(frame.Content) > 4<<20 {
					return access.ErrDenied
				}
				if _, err = store.SaveFile(ctx, handle, frame.Name, frame.Content); err != nil {
					return err
				}
				artifacts[frame.Name] = true
				artifactBytes += len(frame.Content)
			case "done":
				if finished {
					return access.ErrDenied
				}
				finished = true
			default:
				return access.ErrDenied
			}
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return RunProof{}, ctx.Err()
		case <-ticker.C:
			if err = consume(); err != nil {
				return RunProof{}, err
			}
		case <-session.Done():
			if err = consume(); err != nil {
				return RunProof{}, err
			}
			if !finished {
				return RunProof{}, errors.New("restricted response incomplete")
			}
			assistant, err := store.AppendContext(ctx, handle, "assistant", text.String())
			if err != nil {
				return RunProof{}, err
			}
			if _, err = store.Resolve(ctx, handle); err != nil {
				return RunProof{}, err
			}
			if err = store.CompleteAttempt(ctx, handle); err != nil {
				return RunProof{}, err
			}
			if err = store.CheckContextAttempt(ctx, handle); err != nil {
				return RunProof{}, err
			}
			if err = emit("done", ""); err != nil {
				return RunProof{}, err
			}
			complete = true
			return NewRunProof(handle, []string{assistant.ID}), nil
		}
	}
}
