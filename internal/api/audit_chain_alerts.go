package api

// Audit chain incidents reach the people who run the instance.
//
// A detected chain break (an append refused by a workspace or instance chain
// tail, a failed check while listing the instance audit log, or breaks found
// by `journal verify`) and every OWNER reseal produce:
//
//  1. an instance audit event: audit.chain_break_detected (entity
//     audit_chain/<incident key>), chained normally or — when the instance
//     chain tail is itself broken — recorded as an unchained fallback. A
//     reseal's event is written in the reseal's own transaction instead
//     (audit.resealed for the instance chain, audit.workspace_resealed for a
//     workspace chain). One event per incident key: repeats are not recorded.
//  2. an inbox item for every instance admin, routed exactly like backup
//     incidents (listInstanceAdmins: the admin's primary workspace, targeted at
//     them alone), kind=message, payload.subkind="audit_chain_incident",
//     category security, source "audit_chain:<incident key>:<user>".
//
// Detection points run inside writer transactions, so the alert never writes
// there: it is handed to a registered background goroutine that waits for the
// writer lock like any other write.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// SubkindAuditChainIncident discriminates an audit chain incident's inbox item.
const SubkindAuditChainIncident = notify.SubkindAuditChainIncident

type auditChainIncident struct {
	Kind         string // "chain_break" or "reseal"
	Scope        string // "instance" or "workspace"
	WorkspaceID  string
	Seq          int64 // refused tail, first reported break, or resealed old tail
	Reason       string
	CheckpointID string
}

func (i auditChainIncident) key() string {
	if i.Kind == "reseal" {
		return "reseal:" + i.CheckpointID
	}
	if i.Scope == "workspace" {
		return fmt.Sprintf("chain_break:workspace:%s:seq:%d", i.WorkspaceID, i.Seq)
	}
	return fmt.Sprintf("chain_break:instance:seq:%d", i.Seq)
}

// auditIncidentReason names the verdict class first: post_upgrade_invalid is
// damage since the keyring upgrade and must not read like inherited history.
func auditIncidentReason(res *journal.VerifyResult) string {
	if res.HistoricalStatus != "" {
		return res.HistoricalStatus + ": " + res.Reason
	}
	return res.Reason
}

type auditChainAlerter struct {
	db     *sql.DB
	logger *slog.Logger
}

type auditChainAlerterKey struct{}

func withAuditChainAlerter(ctx context.Context, a *auditChainAlerter) context.Context {
	if a == nil {
		return ctx
	}
	return context.WithValue(ctx, auditChainAlerterKey{}, a)
}

// raiseAuditChainIncident alerts through the request's alerter; a context
// without one (local CLI recovery, unit fixtures) only logs.
func raiseAuditChainIncident(ctx context.Context, inc auditChainIncident) {
	if a, _ := ctx.Value(auditChainAlerterKey{}).(*auditChainAlerter); a != nil {
		a.raise(ctx, inc)
		return
	}
	slog.Default().Error("audit chain incident", "kind", inc.Kind, "scope", inc.Scope, "workspace_id", inc.WorkspaceID, "seq", inc.Seq, "reason", inc.Reason)
}

// raise records and delivers inc in the background. ctx supplies only the
// signing-key snapshot; the goroutine's own context carries no alerter, so an
// alert that meets the same broken tail cannot recurse.
func (a *auditChainAlerter) raise(ctx context.Context, inc auditChainIncident) {
	if a == nil || a.db == nil {
		return
	}
	a.logger.Error("audit chain incident", "kind", inc.Kind, "scope", inc.Scope, "workspace_id", inc.WorkspaceID, "seq", inc.Seq, "reason", inc.Reason)
	keys, _ := journal.SigningKeysForContext(ctx)
	done := beginBackgroundWork()
	go func() {
		defer done()
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// A nil boot snapshot stays an explicit unavailable-key sentinel;
		// never reload a different keyring from environment on this path.
		bg = journal.WithSigningKeys(bg, keys)
		if inc.Kind != "reseal" {
			if inc.Scope == "workspace" && inc.Seq == 0 {
				// The writer reports from inside its transaction; the refused
				// tail is read here, outside it. The tail cannot move while
				// it refuses appends, so the key stays stable.
				_ = a.db.QueryRowContext(bg, `SELECT COALESCE(MAX(seq),0) FROM journal_entries WHERE workspace_id=? AND seq>0`, inc.WorkspaceID).Scan(&inc.Seq)
			}
			_, err := a.record(bg, inc)
			if err != nil {
				a.logger.Error("audit chain incident: record instance audit event", "key", inc.key(), "err", err)
				return
			}
		}
		// An existing incident is not evidence of successful delivery. Give
		// delivery its own budget and let the inbox source deduplicate it.
		cancel()
		delivery, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		a.notifyAdmins(journal.WithSigningKeys(delivery, keys), inc)
	}()
}

// record appends audit.chain_break_detected once per incident key. The
// existence check runs inside the writer transaction, so concurrent reports of
// one incident record one event.
func (a *auditChainAlerter) record(ctx context.Context, inc auditChainIncident) (bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck
	var seen bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM instance_audit_logs WHERE entity_type='audit_chain' AND entity_id=? AND action='audit.chain_break_detected')`, inc.key()).Scan(&seen); err != nil || seen {
		return false, err
	}
	raw, err := json.Marshal(map[string]any{"scope": inc.Scope, "workspace_id": inc.WorkspaceID, "seq": inc.Seq, "reason": inc.Reason})
	if err != nil {
		return false, err
	}
	f := journal.InstanceAuditFields{ID: generateCUID(), Action: "audit.chain_break_detected", EntityType: "audit_chain", EntityID: sql.NullString{String: inc.key(), Valid: true}, TargetWorkspaceID: sql.NullString{String: inc.WorkspaceID, Valid: inc.WorkspaceID != ""}, Metadata: sql.NullString{String: string(raw), Valid: true}, CreatedAt: tsformat.Format(time.Now())}
	// The incident record is the alert itself: like a revocation, it must
	// not be held hostage by the broken tail it reports.
	if err := appendInstanceAuditTx(withSecurityAuditFallback(ctx), tx, f); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (a *auditChainAlerter) notifyAdmins(ctx context.Context, inc auditChainIncident) {
	admins, err := listInstanceAdmins(ctx, a.db)
	if err != nil {
		a.logger.Error("audit chain incident: list instance admins", "key", inc.key(), "err", err)
		return
	}
	title, body := "Audit chain needs attention", ""
	scope := "the instance audit chain"
	if inc.Scope == "workspace" {
		scope = "the audit journal of workspace " + inc.WorkspaceID
	}
	if inc.Kind == "reseal" {
		title = "Audit chain resealed"
		body = fmt.Sprintf("An OWNER started a new authenticated segment of %s after seq %d. Historical rows were not rewritten.", scope, inc.Seq)
	} else {
		body = fmt.Sprintf("An integrity check failed on %s at seq %d. Inspect the audit history before an OWNER reseals it.", scope, inc.Seq)
		if inc.Reason != "" {
			body += "\n\n" + inc.Reason
		}
	}
	payload := map[string]any{"subkind": SubkindAuditChainIncident, "incident_kind": inc.Kind, "incident_key": inc.key(), "scope": inc.Scope, "seq": inc.Seq, "view_url": "/admin?tab=audit"}
	if inc.WorkspaceID != "" {
		payload["workspace_id"] = inc.WorkspaceID
	}
	for _, ad := range admins {
		if ad.WorkspaceID == "" {
			a.logger.Info("audit chain incident: instance admin belongs to no workspace, so has no inbox", "user", ad.Email, "key", inc.key())
			continue
		}
		err := a.deliver(ctx, ad.ID, inc.key(), inbox.Item{
			WorkspaceID: ad.WorkspaceID, Kind: inbox.KindMessage, SourceID: "audit_chain:" + inc.key() + ":" + ad.ID,
			TargetUserID: ad.ID, Title: title, BodyMD: body, SenderType: "system", SenderName: "Audit",
			Priority: "high", Payload: payload, Category: notify.CategorySecurity, AttentionClass: inbox.AttentionRepair,
		})
		if err != nil {
			a.logger.Error("audit chain incident: deliver inbox notice", "key", inc.key(), "user_id", ad.ID, "err", err)
		}
	}
}

// retryAuditNotices rebuilds missing inbox projections from durable incident
// and reseal events, including after a process exit or a subsequently repaired
// chain. Delivery receipts outlive inbox retention and prevent replay from
// resurrecting a handled notice or sending its external notification again.
func (r *Router) retryAuditNotices(ctx context.Context) error {
	rows, err := r.db.QueryContext(ctx, `SELECT id,action,COALESCE(target_workspace_id,''),COALESCE(metadata,'{}') FROM instance_audit_logs WHERE entity_type='audit_chain' AND action IN ('audit.chain_break_detected','audit.resealed','audit.workspace_resealed') ORDER BY created_at,id`)
	if err != nil {
		return err
	}
	var incidents []auditChainIncident
	for rows.Next() {
		var id, action, workspace, raw string
		if err := rows.Scan(&id, &action, &workspace, &raw); err != nil {
			rows.Close()
			return err
		}
		var data struct {
			Scope        string                   `json:"scope"`
			WorkspaceID  string                   `json:"workspace_id"`
			Seq          int64                    `json:"seq"`
			Reason       string                   `json:"reason"`
			CheckpointID string                   `json:"checkpoint_id"`
			OldTailSeq   int64                    `json:"old_tail_seq"`
			Checkpoint   journal.ResealCheckpoint `json:"checkpoint"`
		}
		if err := json.Unmarshal([]byte(raw), &data); err != nil {
			r.logger.Error("audit chain incident: malformed replay metadata", "id", id, "err", err)
			continue
		}
		inc := auditChainIncident{Kind: "chain_break", Scope: data.Scope, WorkspaceID: data.WorkspaceID, Seq: data.Seq, Reason: data.Reason}
		if action == "audit.resealed" {
			inc = auditChainIncident{Kind: "reseal", Scope: "instance", CheckpointID: id, Seq: data.Checkpoint.OldTailSeq, Reason: data.Checkpoint.Reason}
		} else if action == "audit.workspace_resealed" {
			inc = auditChainIncident{Kind: "reseal", Scope: "workspace", WorkspaceID: workspace, CheckpointID: data.CheckpointID, Seq: data.OldTailSeq, Reason: data.Reason}
		}
		incidents = append(incidents, inc)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, inc := range incidents {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.auditAlerts.notifyAdmins(ctx, inc)
	}
	return nil
}

// deliver commits the inbox projection and its independent delivery receipt
// together. Inbox purge/retention must never reset delivery history. The fast
// read avoids acquiring a writer for an already delivered incident; the second
// check under the production BEGIN IMMEDIATE makes concurrent deliveries safe.
func (a *auditChainAlerter) deliver(ctx context.Context, user, key string, item inbox.Item) error {
	var delivered bool
	const seen = `SELECT EXISTS(SELECT 1 FROM instance_audit_notice_deliveries WHERE incident_key=? AND user_id=?)`
	if err := a.db.QueryRowContext(ctx, seen, key, user).Scan(&delivered); err != nil || delivered {
		return err
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := tx.QueryRowContext(ctx, seen, key, user).Scan(&delivered); err != nil || delivered {
		return err
	}
	// Existing projections from an earlier running version are adopted without
	// another external send. Normal first delivery creates both rows atomically.
	var existed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_items WHERE kind=? AND source_id=?)`, item.Kind, item.SourceID).Scan(&existed); err != nil {
		return err
	}
	if err := inbox.InsertTx(ctx, tx, a.logger, item); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO instance_audit_notice_deliveries(incident_key,user_id,delivered_at) VALUES(?,?,?)`, key, user, tsformat.Format(time.Now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !existed {
		inbox.NotifyExternalChannels(ctx, item)
	}
	return nil
}
