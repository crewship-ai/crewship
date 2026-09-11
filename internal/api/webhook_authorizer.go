package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/dispatch"
)

// WebhookAuthorizer re-checks at dispatch what acceptance checked at
// acceptance, and it is required in production.
//
// The gap between the two can be long — work waits for capacity — and in that
// gap an agent can be deleted or disabled, a crew removed, a workspace's budget
// exhausted. §3's I8 asks for the check at both ends precisely because the
// answer is allowed to change in between, and handing the runtime what
// acceptance saw would be checking one end of the gap twice.
//
// A refusal here is not an error. It is the system declining to run work it is
// no longer permitted to run, and the reason it returns is what a user is shown
// in place of a result.
type WebhookAuthorizer struct {
	db *sql.DB
}

func NewWebhookAuthorizer(db *sql.DB) *WebhookAuthorizer { return &WebhookAuthorizer{db: db} }

// Authorize returns a refusal reason, or "" to proceed.
//
// An error means the question could not be answered, which is NOT permission.
// The dispatcher parks the work rather than guessing in either direction — and
// the distinction matters in both: reading an unavailable database as "allowed"
// runs work nobody authorised, and reading it as "refused" fails work that was
// perfectly fine.
func (a *WebhookAuthorizer) Authorize(ctx context.Context, as dispatch.Assignment) (string, error) {
	if as.Item.AgentID == "" {
		return "the work names no agent", nil
	}

	var (
		deletedAt   sql.NullString
		crewID      sql.NullString
		workspaceID string
	)
	err := a.db.QueryRowContext(ctx,
		`SELECT deleted_at, crew_id, workspace_id FROM agents WHERE id = ?`, as.Item.AgentID).
		Scan(&deletedAt, &crewID, &workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Sprintf("agent %s no longer exists", as.Item.AgentID), nil
	}
	if err != nil {
		return "", fmt.Errorf("read agent %s at dispatch: %w", as.Item.AgentID, err)
	}
	if deletedAt.Valid && deletedAt.String != "" {
		return fmt.Sprintf("agent %s was deleted while this work waited", as.Item.AgentID), nil
	}

	// The work was accepted for a workspace. An agent that has since moved to
	// another one must not run it: the delivery's authorization belonged to the
	// workspace it arrived in.
	if workspaceID != as.Item.WorkspaceID {
		return fmt.Sprintf("agent %s now belongs to a different workspace than the work that named it",
			as.Item.AgentID), nil
	}

	if crewID.Valid && crewID.String != "" {
		var crewDeleted sql.NullString
		err := a.db.QueryRowContext(ctx,
			`SELECT deleted_at FROM crews WHERE id = ?`, crewID.String).Scan(&crewDeleted)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Sprintf("the crew agent %s belongs to no longer exists", as.Item.AgentID), nil
		}
		if err != nil {
			return "", fmt.Errorf("read crew %s at dispatch: %w", crewID.String, err)
		}
		if crewDeleted.Valid && crewDeleted.String != "" {
			return fmt.Sprintf("the crew agent %s belongs to was deleted while this work waited",
				as.Item.AgentID), nil
		}
	}

	return "", nil
}

var _ dispatch.Authorizer = (*WebhookAuthorizer)(nil)
