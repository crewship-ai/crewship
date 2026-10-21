package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func captureHumanIssueOrigin(ctx context.Context, tx *sql.Tx, r *http.Request, missionID string) (string, error) {
	user := UserFromContext(r.Context())
	if user == nil {
		return "", runauthority.ErrUnavailable
	}
	// Scoped CRUD is allowed, but cannot create a durable execution grant.
	// Explicit execution endpoints reject this mode before mutating work.
	if limitedChatToken(r.Context()) {
		return "", nil
	}
	origin, err := runauthority.CaptureWebOrigin(ctx, tx, "web_issue", missionID, user.ID, WorkspaceIDFromContext(r.Context()))
	return origin.ID, err
}
