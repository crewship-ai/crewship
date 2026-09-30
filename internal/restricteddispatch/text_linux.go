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

// TextRunner owns the safe text-only application path. The manager must use
// this same authority and the fixed production restricted-runner image.
type TextSession interface {
	Output(context.Context) (string, error)
	Done() <-chan struct{}
	Stop(string)
}

type TextRunner struct {
	StartSession    func(context.Context, string) (TextSession, error)
	Authority       Authority
	Manager         *restrictedruntime.Manager
	MaxOutputTokens int64
}

func (r *TextRunner) Execute(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, nil, emit, false)
}

// ExecuteRun is selected only by the dedicated authenticated CLI/run route.
func (r *TextRunner) ExecuteRun(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, nil, emit, true)
}

// ExecuteRunWithRights preserves additional server-classified source rights on
// each fresh workflow step. Public request bodies cannot select these rights.
func (r *TextRunner) ExecuteRunWithRights(ctx context.Context, user, workspace, chat, input string, rights []access.Right, emit func(string, string) error) error {
	return r.execute(ctx, user, workspace, chat, input, rights, emit, true)
}
func (r *TextRunner) execute(ctx context.Context, user, workspace, chat, input string, rights []access.Right, emit func(string, string) error, runOperation bool) error {
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
	if err := store.DB.QueryRowContext(ctx, `SELECT c.agent_id,a.restricted_execution_profile FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id WHERE c.id=? AND c.workspace_id=? AND c.visibility IN ('private','group')`, chat, workspace).Scan(&agent, &profile); err != nil {
		return access.ErrDenied
	}
	if profile != "responses_text" {
		return access.ErrDenied
	}
	prepare := r.Authority.PrepareChatResponses
	if runOperation {
		prepare = r.Authority.PrepareResponses
	}
	handle, _, err := prepare(ctx, user, workspace, agent, chat, "", rights, r.MaxOutputTokens, func(ctx context.Context, attempt access.Attempt) ([]string, error) {
		binding, err := loadProvider(ctx, store.DB, attempt.ID)
		if err != nil {
			return nil, err
		}
		if binding.Profile != "responses_text" {
			return nil, access.ErrDenied
		}
		prompt, err := store.BuildContext(ctx, attempt, input)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(map[string]any{"model": binding.Model, "instructions": prompt.System, "input": prompt.Input, "store": false, "stream": true, "max_output_tokens": binding.MaxOutputTokens})
		if err != nil {
			return nil, err
		}
		return []string{"/opt/crewship-runner", "responses", string(body)}, nil
	})
	if err != nil {
		return err
	}
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
	if _, err = store.AppendContext(ctx, handle, "user", input); err != nil {
		return err
	}
	start := r.StartSession
	if start == nil {
		start = func(ctx context.Context, handle string) (TextSession, error) { return r.Manager.Start(ctx, handle) }
	}
	session, err := start(ctx, handle)
	if err != nil {
		return err
	}
	defer session.Stop("application_finished")
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	offset := 0
	var text strings.Builder
	finished := false
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
				Type string `json:"type"`
				Text string `json:"text"`
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
			return ctx.Err()
		case <-ticker.C:
			if err = consume(); err != nil {
				return err
			}
		case <-session.Done():
			if err = consume(); err != nil {
				return err
			}
			if !finished {
				return errors.New("restricted response incomplete")
			}
			_, err = store.AppendContext(ctx, handle, "assistant", text.String())
			if err != nil {
				return err
			}
			if _, err = store.Resolve(ctx, handle); err != nil {
				return err
			}
			if err = store.CompleteAttempt(ctx, handle); err != nil {
				return err
			}
			if err = store.CheckContextAttempt(ctx, handle); err != nil {
				return err
			}
			if err = emit("done", ""); err != nil {
				return err
			}
			complete = true
			return nil
		}
	}
}
