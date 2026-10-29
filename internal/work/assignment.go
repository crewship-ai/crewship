package work

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// AssignmentInput is immutable acceptance input. Runtime configuration and
// permissions are deliberately resolved again after a claim.
type AssignmentInput struct {
	Version       int    `json:"version"`
	AssignmentID  string `json:"assignment_id"`
	WorkspaceID   string `json:"workspace_id"`
	AgentID       string `json:"agent_id"`
	CrewID        string `json:"crew_id"`
	AuthorAgentID string `json:"author_agent_id"`
	Task          string `json:"task"`
}

func AcceptAssignmentTx(ctx context.Context, tx *sql.Tx, id string) (Receipt, error) {
	var in AssignmentInput
	var session, author string
	in.Version, in.AssignmentID = 1, id
	err := tx.QueryRowContext(ctx, `SELECT a.workspace_id,a.assigned_to_id,COALESCE(ag.crew_id,''),a.task,COALESCE(a.session_id,''),COALESCE(a.created_by_user_id,''),COALESCE(a.assigned_by_id,'') FROM assignments a JOIN agents ag ON ag.id=a.assigned_to_id AND ag.workspace_id=a.workspace_id WHERE a.id=? AND a.status IN ('PENDING','QUEUED')`, id).Scan(&in.WorkspaceID, &in.AgentID, &in.CrewID, &in.Task, &session, &author, &in.AuthorAgentID)
	if err != nil {
		return Receipt{}, fmt.Errorf("assignment work identity: %w", err)
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return Receipt{}, err
	}
	store := NewStore(nil)
	if err = store.CheckIngressTx(ctx, tx, DefaultIngressLimits(), IngressRequest{WorkspaceID: in.WorkspaceID, BodyBytes: int64(len(raw))}); err != nil {
		return Receipt{}, err
	}
	digest := sha256.Sum256(raw)
	return store.AcceptTx(ctx, tx, AcceptRequest{
		WorkspaceID: in.WorkspaceID, Source: SourceAssignment, SourceRef: id,
		DomainKind: DomainAssignment, DomainID: id, AgentID: in.AgentID, CrewID: in.CrewID,
		SessionID: session, Class: ClassBackground, AuthorizedByUserID: author,
		AuthorizedScope: "assignment:" + id, InputJSON: string(raw), InputSHA256: hex.EncodeToString(digest[:]),
	})
}
