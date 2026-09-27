package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrInvokingUserNotMember stops human-triggered work after membership
// revocation. It is only the membership floor: role, capability, Page action
// and future resource grants still need their own execution-time policies.
var ErrInvokingUserNotMember = errors.New("routine invoking user is no longer a workspace member")

// checkInvokingUser uses the persisted trigger identity, including deferred,
// resumed and nested runs. No cached authorization is carried across waits.
// Unattended triggers have no human actor and retain their existing gates.
// Bare test executors may omit memberCheck; production always wires it.
// Revocation stops subsequent dispatches, not an already running operation.
func (e *Executor) checkInvokingUser(ctx context.Context, in RunInput) error {
	if in.InvokingUserID == "" || in.Mode == ModeDryRun || e.memberCheck == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	member, err := e.memberCheck(ctx, in.WorkspaceID, in.InvokingUserID)
	if err != nil {
		return fmt.Errorf("routine invoking user membership check: %w", err)
	}
	if !member {
		return ErrInvokingUserNotMember
	}
	return nil
}
