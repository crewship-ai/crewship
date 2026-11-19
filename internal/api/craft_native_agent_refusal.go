package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// Reasons a native Craft agent task start is refused (#5596). The requester
// learns one only after Page authority passed: a caller who cannot see and
// edit the Page keeps the generic refusal, so no reason is an existence or
// policy oracle. docs/specs/craft-native.md lists them; the CLI accepts only
// these values.
const (
	craftNativeAgentReasonPolicyDisabled         = "workspace_policy_disabled"
	craftNativeAgentReasonCrewNotAllowed         = "crew_not_allowed"
	craftNativeAgentReasonChatUnavailable        = "chat_unavailable"
	craftNativeAgentReasonChatNotPrivate         = "chat_not_private"
	craftNativeAgentReasonInstructionNotRecorded = "instruction_not_recorded"
	craftNativeAgentReasonInstructionUnusable    = "instruction_unusable"
	craftNativeAgentReasonAgentUnavailable       = "agent_unavailable"
	craftNativeAgentReasonNotOwnerOrDelegated    = "not_owner_or_delegated"
	craftNativeAgentReasonDuplicateTask          = "duplicate_task"
	craftNativeAgentReasonCapacity               = "capacity"
	craftNativeAgentReasonCredentialExpiring     = "credential_expiring"
	craftNativeAgentReasonClockUnavailable       = "clock_unavailable"
	craftNativeAgentReasonTaskTooLarge           = "task_too_large"
)

// craftNativeAgentRefusal is a task-start refusal its requester may be told
// about. It wraps errCraftNativeAgentDenied, so every existing refusal check
// still sees the mandate denial.
type craftNativeAgentRefusal struct{ reason string }

func (e craftNativeAgentRefusal) Error() string {
	return errCraftNativeAgentDenied.Error() + ": " + e.reason
}
func (e craftNativeAgentRefusal) Unwrap() error { return errCraftNativeAgentDenied }

// craftNativeAgentRefusalReason is the disclosed reason, or "" for a refusal
// that stays generic.
func craftNativeAgentRefusalReason(err error) string {
	var refusal craftNativeAgentRefusal
	if errors.As(err, &refusal) {
		return refusal.reason
	}
	return ""
}

// craftNativeAgentTaskError answers a task start refusal. A reasoned refusal
// adds "reason" to the {"error"} body; every other error keeps the native
// Craft document mapping unchanged.
func craftNativeAgentTaskError(w http.ResponseWriter, err error) {
	if reason := craftNativeAgentRefusalReason(err); reason != "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "native Craft agent task denied", "reason": reason})
		return
	}
	craftNativeDocumentError(w, err)
}

// craftNativeAgentInstructionReason explains why the recorded-instruction
// lookup found nothing. It reads only the requester's own chat, message and
// that chat's agent, which CaptureTrustedQuery already showed them; any
// failure here keeps the refusal generic.
func craftNativeAgentInstructionReason(ctx context.Context, tx *sql.Tx, input craftNativeAgentTaskRequest, user, agent, ws string) string {
	var private, recorded, live bool
	err := tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM chats WHERE id=? AND created_by=? AND visibility='private'),
 EXISTS(SELECT 1 FROM conversation_messages WHERE id=? AND session_id=? AND author_user_id=? AND agent_id=? AND role='user' AND native_craft_instruction=1),
 EXISTS(SELECT 1 FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL)`,
		input.ChatID, user, input.MessageID, input.ChatID, user, agent, agent, ws).Scan(&private, &recorded, &live)
	switch {
	case err != nil:
		return ""
	case !private:
		return craftNativeAgentReasonChatNotPrivate
	case !recorded:
		return craftNativeAgentReasonInstructionNotRecorded
	case !live:
		return craftNativeAgentReasonAgentUnavailable
	default:
		// Empty, oversized or not valid text.
		return craftNativeAgentReasonInstructionUnusable
	}
}

// craftNativeAgentPolicyReason tells a disabled (or never saved) workspace
// agent artifact policy apart from one that excludes the agent's crew.
func craftNativeAgentPolicyReason(ctx context.Context, tx *sql.Tx, ws string) string {
	var enabled bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_artifact_creation_policy WHERE workspace_id=? AND enabled=1)`, ws).Scan(&enabled); err != nil {
		return ""
	}
	if !enabled {
		return craftNativeAgentReasonPolicyDisabled
	}
	return craftNativeAgentReasonCrewNotAllowed
}
