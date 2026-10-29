package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Workbook custody.
//
// The custody API (page-ownership list and transfer) exists so that a
// departing person's private outputs reach a human successor. For a workbook
// that is a stronger act than for a Doc: the successor gains write and delete
// rights over content that has no backup. So, for a Page holding a workbook:
//
//   - only a workspace OWNER with a current human credential is a custodian
//     (an ADMIN never is, whatever the Page's audience), and
//   - custody opens only once the owner is no longer a live member: removed
//     from the workspace, suspended, restricted (Guest), or with a removal or
//     erasure that was refused because of this workbook within
//     workbookCustodyRequestTTL. The refused departure is recorded as an
//     audit_logs row (workbookCustodyRequestedAction) by
//     recordWorkbookCustodyRequests; there is no other departure marker.
//
// workbookCustodySQL states the rule once; the listing filters on it and the
// transfer UPDATE repeats it. Every workbook custody transfer writes an
// audit_logs row (workbookCustodyTransferredAction) in the same transaction.
const (
	workbookCustodyRequestedAction   = "artifact.workbook_custody_requested"
	workbookCustodyTransferredAction = "artifact.workbook_custody_transferred"
	// A refused removal or erasure keeps the workbook's custody open for this
	// long; retrying the removal or erasure opens it again.
	workbookCustodyRequestTTL = 7 * 24 * time.Hour
)

const workbookCustodyRefusal = "workbook custody opens only to a workspace OWNER once the workbook's owner has left the workspace, is suspended, or a removal or erasure of the owner was refused because of this workbook in the last 7 days; while the owner is an active member only they can hand it over"

// workbookCustodySQL is true for a Page that is not a workbook, or for a
// workbook whose custody is open to actorID (see the file comment). kind is
// privatePageOwnershipSQL's kind expression for the same alias. The returned
// arguments bind the placeholders in order.
func workbookCustodySQL(alias, kind, actorID string, now time.Time) (string, []any) {
	actorIsOwner := `EXISTS(SELECT 1 FROM workspace_members wca JOIN users wcu ON wcu.id=wca.user_id AND wcu.suspended_at IS NULL WHERE wca.workspace_id=` + alias + `.workspace_id AND wca.user_id=? AND wca.role='OWNER')`
	ownerLive := `EXISTS(SELECT 1 FROM workspace_members wco JOIN users wcou ON wcou.id=wco.user_id AND wcou.suspended_at IS NULL WHERE wco.workspace_id=` + alias + `.workspace_id AND wco.user_id=` + alias + `.owner_user_id AND NOT EXISTS(SELECT 1 FROM workspace_members wcr WHERE wcr.user_id=wco.user_id AND wcr.access_mode='restricted'))`
	departing := `EXISTS(SELECT 1 FROM audit_logs wcq WHERE wcq.workspace_id=` + alias + `.workspace_id AND wcq.entity_type='page' AND wcq.entity_id=` + alias + `.id AND wcq.action='` + workbookCustodyRequestedAction + `' AND json_extract(wcq.metadata,'$.owner_user_id')=` + alias + `.owner_user_id AND wcq.created_at>=?)`
	return `((` + kind + `)<>'workbook' OR (` + actorIsOwner + ` AND (NOT ` + ownerLive + ` OR ` + departing + `)))`,
		[]any{actorID, tsformat.Format(now.Add(-workbookCustodyRequestTTL))}
}

// recordWorkbookCustodyRequests opens custody of the workbooks among pageIDs
// that owner still owns in ws, after a removal or erasure of owner was refused
// for them. The departure's own transaction has rolled back, so this commits
// separately; callers must not hold an open transaction on db.
func recordWorkbookCustodyRequests(ctx context.Context, db *sql.DB, j journal.Emitter, actorID, ws, owner, reason string, pageIDs []string) error {
	if len(pageIDs) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	_, kind, err := privatePageOwnershipSQL(ctx, tx, "p")
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(map[string]string{"owner_user_id": owner, "reason": reason})
	now := tsformat.Format(time.Now())
	var notifications []func()
	for _, id := range pageIDs {
		result, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) SELECT ?,p.workspace_id,(SELECT id FROM users WHERE id=?),?,'page',p.id,?,? FROM pages p WHERE p.id=? AND p.workspace_id=? AND p.owner_user_id=? AND (`+kind+`)='workbook'`,
			generateCUID(), actorID, workbookCustodyRequestedAction, string(metadata), now, id, ws, owner)
		if err != nil {
			return fmt.Errorf("record workbook custody request: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			continue
		}
		notify, err := appendAuthorizationAudit(ctx, tx, j, journal.Entry{
			WorkspaceID: ws, Type: workbookCustodyRequestedAction, ActorType: journal.ActorUser, ActorID: actorID,
			Summary: "Workbook custody requested", Payload: map[string]any{"page_id": id, "owner_user_id": owner, "reason": reason},
		})
		if err != nil {
			return err
		}
		notifications = append(notifications, notify)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, notify := range notifications {
		notify()
	}
	return nil
}

// recordRefusedDepartureCustody is the departure paths' hook: when err is a
// manual-transfer refusal it records the workbook custody requests and
// returns err unchanged, or the recording failure.
func recordRefusedDepartureCustody(ctx context.Context, db *sql.DB, j journal.Emitter, actorID, ws, owner, reason string, err error) error {
	var manual *ErrPagesNeedManualTransfer
	if !errors.As(err, &manual) {
		return err
	}
	if recordErr := recordWorkbookCustodyRequests(ctx, db, j, actorID, ws, owner, reason, manual.PageIDs); recordErr != nil {
		return recordErr
	}
	return err
}
