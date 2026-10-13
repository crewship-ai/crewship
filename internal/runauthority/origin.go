package runauthority

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// LegacyUnverifiedDelegationRootRunID identifies the permanently invalid upgrade
// fence for work admitted before durable delegation provenance existed.
const LegacyUnverifiedDelegationRootRunID = "legacy-unverified-delegation:20261013162000"

// Origin is immutable host state. It preserves the requesting human's original
// chat receipt separately from the agent that executes delegated work.
type Origin struct {
	ID, WorkspaceID, RootRunID, ChatID, TriggerMessageID string
	HumanAuthority                                       *chataudience.Receipt
}

type originKey struct{}

func WithOriginID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, originKey{}, id)
}

func OriginIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(originKey{}).(string)
	return id
}

func loadOrigin(ctx context.Context, q Queryer, id, workspaceID string) (Origin, error) {
	var origin Origin
	var raw string
	err := q.QueryRowContext(ctx, `SELECT id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority FROM execution_origins WHERE id=? AND workspace_id=?`, id, workspaceID).Scan(&origin.ID, &origin.WorkspaceID, &origin.RootRunID, &origin.ChatID, &origin.TriggerMessageID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return origin, ErrUnavailable
	}
	if err != nil {
		return origin, err
	}
	if json.Unmarshal([]byte(raw), &origin.HumanAuthority) != nil || origin.HumanAuthority == nil {
		return origin, ErrUnavailable
	}
	r := origin.HumanAuthority
	if origin.RootRunID == "" || origin.ChatID == "" || origin.TriggerMessageID == "" || r.UserID == "" || r.AgentID == "" || r.WorkspaceID != workspaceID || r.ChatID != origin.ChatID {
		return origin, ErrIdentityMismatch
	}
	return origin, nil
}

// CaptureOrigin must share the admission transaction with the child resource.
// Source identity comes from the sidecar's verified token, never tool arguments.
func CaptureOrigin(ctx context.Context, tx *sql.Tx, activeRunID, actorAgentID, workspaceID string) (Origin, error) {
	if tx == nil {
		return Origin{}, ErrUnavailable
	}
	a, err := Resolve(ctx, tx, activeRunID, actorAgentID, workspaceID)
	if err != nil {
		return Origin{}, err
	}
	if a.HumanAuthority == nil {
		return Origin{}, nil
	}
	if a.ChatOriginID != "" {
		return loadOrigin(ctx, tx, a.ChatOriginID, workspaceID)
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return Origin{}, err
	}
	id := "c" + hex.EncodeToString(entropy[:])
	raw, err := json.Marshal(a.HumanAuthority)
	if err != nil {
		return Origin{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(workspace_id,root_run_id) DO NOTHING`, id, workspaceID, activeRunID, a.ChatID, a.TriggerMessageID, string(raw), tsformat.Format(time.Now())); err != nil {
		return Origin{}, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM execution_origins WHERE workspace_id=? AND root_run_id=?`, workspaceID, activeRunID).Scan(&id); err != nil {
		return Origin{}, err
	}
	origin, err := loadOrigin(ctx, tx, id, workspaceID)
	if err != nil {
		return Origin{}, err
	}
	if *origin.HumanAuthority != *a.HumanAuthority || origin.ChatID != a.ChatID || origin.TriggerMessageID != a.TriggerMessageID {
		return Origin{}, ErrIdentityMismatch
	}
	return origin, nil
}

// ValidateOriginReceipt checks authoring and publication provenance before an
// executor exists. Execution must additionally use ValidateOrigin.
func ValidateOriginReceipt(ctx context.Context, q Queryer, originID, workspaceID string) (Origin, error) {
	if q == nil || originID == "" || workspaceID == "" {
		return Origin{}, ErrUnavailable
	}
	origin, err := loadOrigin(ctx, q, originID, workspaceID)
	if err != nil {
		return origin, err
	}
	live, err := chataudience.CaptureTrustedQuery(ctx, q, origin.ChatID, origin.HumanAuthority.UserID, origin.HumanAuthority)
	if err != nil {
		return origin, err
	}
	if live == nil {
		return origin, ErrRequesterStale
	}
	origin.HumanAuthority = live
	return origin, nil
}

// ValidateOrigin rechecks the root receipt even after the parent has finished.
// It never substitutes the executor's identity into the human's receipt.
func ValidateOrigin(ctx context.Context, q Queryer, originID, executorAgentID, workspaceID string) (Authority, error) {
	a := Authority{AgentID: executorAgentID, WorkspaceID: workspaceID, ChatOriginID: originID}
	if q == nil || originID == "" || executorAgentID == "" || workspaceID == "" {
		return a, ErrUnavailable
	}
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL)`, executorAgentID, workspaceID).Scan(&exists); err != nil {
		return a, err
	}
	if !exists {
		return a, ErrIdentityMismatch
	}
	origin, err := ValidateOriginReceipt(ctx, q, originID, workspaceID)
	if err != nil {
		return a, err
	}
	a.ChatID, a.TriggerMessageID, a.HumanAuthority = origin.ChatID, origin.TriggerMessageID, origin.HumanAuthority
	return a, nil
}
