// Package restricteddispatch connects durable application authority to the
// isolated executor. It never accepts runtime plans from client task metadata.
package restricteddispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// Authority supports offline commands and explicitly prepared text Responses
// operations. Persistent mounts and agent-visible credentials remain denied;
// callers cannot supply a broader Plan or fall back to a shared crew process.
type Authority struct{ Store access.Store }

const cleanupTimeout = 3 * time.Second

// BuildCommand is trusted server code, called only after admission. Implementors
// must select prompt/recall from the provided attempt's rights and scope, never
// from the shared agent/crew configuration. The return value is frozen for this
// attempt; renewal never reconstructs a prompt from newer or broader context.
type BuildCommand func(context.Context, access.Attempt) ([]string, error)

// Prepare is a host-only boundary. The caller authenticates user; public task
// JSON must not populate it, parent, or build. A failed preparation revokes its
// attempt, so it cannot later be resumed with a different payload.
func (a Authority) Prepare(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, build BuildCommand) (string, access.Attempt, error) {
	return a.prepare(ctx, user, workspace, agent, chat, parent, rights, build, false)
}

func (a Authority) PrepareChat(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, build BuildCommand) (string, access.Attempt, error) {
	return a.prepare(ctx, user, workspace, agent, chat, parent, rights, build, true)
}
func (a Authority) prepare(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, build BuildCommand, chatOperation bool) (string, access.Attempt, error) {
	if build == nil {
		return "", access.Attempt{}, access.ErrDenied
	}
	admit := a.Store.Admit
	if chatOperation {
		admit = a.Store.AdmitChat
	}
	handle, attempt, err := admit(ctx, user, workspace, agent, chat, parent, rights)
	if err != nil {
		return "", access.Attempt{}, fmt.Errorf("admit isolated attempt: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			// A canceled request must not leave a usable authority behind.
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer cancel()
			_ = a.Store.RevokeAttempt(cleanup, handle)
		}
	}()
	command, err := build(ctx, attempt)
	if err != nil {
		return "", access.Attempt{}, fmt.Errorf("build isolated command: %w", err)
	}
	if !validCommand(command) {
		return "", access.Attempt{}, access.ErrDenied
	}
	raw, err := json.Marshal(command)
	if err != nil || len(raw) > 131072 {
		return "", access.Attempt{}, access.ErrDenied
	}
	if _, err = a.Store.Resolve(ctx, handle); err != nil {
		return "", access.Attempt{}, fmt.Errorf("authorize prepared command: %w", err)
	}
	if _, err = a.Store.DB.ExecContext(ctx, `INSERT INTO restricted_launches(attempt_id,command_json) VALUES(?,?)`, attempt.ID, string(raw)); err != nil {
		return "", access.Attempt{}, fmt.Errorf("persist isolated command: %w", err)
	}
	complete = true
	return handle, attempt, nil
}

func validCommand(command []string) bool {
	if len(command) == 0 || len(command) > 64 || command[0] == "" {
		return false
	}
	for _, arg := range command {
		if len(arg) > 64*1024 || strings.ContainsRune(arg, 0) {
			return false
		}
	}
	return true
}

// Resolve is called by the real runtime before launch, lease renewal and output.
// Store.Resolve checks all ancestors; no parent capability is persisted here.
func (a Authority) Resolve(ctx context.Context, handle string) (restrictedruntime.Plan, error) {
	attempt, err := a.Store.Resolve(ctx, handle)
	if err != nil {
		return restrictedruntime.Plan{}, fmt.Errorf("resolve isolated authority: %w", err)
	}
	var raw string
	if err = a.Store.DB.QueryRowContext(ctx, `SELECT command_json FROM restricted_launches WHERE attempt_id=?`, attempt.ID).Scan(&raw); err != nil {
		return restrictedruntime.Plan{}, fmt.Errorf("resolve isolated command: %w", err)
	}
	var command []string
	if len(raw) > 131072 || json.Unmarshal([]byte(raw), &command) != nil || !validCommand(command) {
		return restrictedruntime.Plan{}, access.ErrDenied
	}
	plan := restrictedruntime.Plan{
		Workspace: attempt.Workspace, Principal: attempt.Principal, PrincipalKind: "human",
		Agent: attempt.Agent, Scope: attempt.Scope, Attempt: attempt.ID,
		Origin: "chat", OriginID: attempt.Chat, Revision: strconv.FormatInt(attempt.Revision, 10),
		Generation: uint64(attempt.Generation), Mode: "restricted", Expires: attempt.Expires,
		Command: command,
	}
	if err := a.attachProvider(ctx, attempt, &plan); err != nil {
		return restrictedruntime.Plan{}, err
	}
	if err := a.attachNative(ctx, attempt, &plan); err != nil {
		return restrictedruntime.Plan{}, err
	}
	// Binding removal and provider/grant updates revoke in the same mutation
	// transaction. Fence changes that happened during command/binding reads.
	if _, err := a.Store.Resolve(ctx, handle); err != nil {
		return restrictedruntime.Plan{}, err
	}
	return plan, nil
}

func (a Authority) Secrets(ctx context.Context, handle string) (map[string]string, error) {
	if _, err := a.Resolve(ctx, handle); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

// Volume denies every mount until a quota-enforcing application catalog exists.
func (a Authority) Volume(context.Context, restrictedruntime.Plan, restrictedruntime.Mount) (string, error) {
	return "", access.ErrDenied
}

var _ restrictedruntime.Authority = Authority{}
var _ restrictedruntime.Catalog = Authority{}
