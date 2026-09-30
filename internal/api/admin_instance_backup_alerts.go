package api

// Backup incidents reach the people who run the instance.
//
// The inbox is workspace-scoped (every inbox_items row has a workspace_id,
// and the list endpoint reads the caller's current workspace), while a backup
// incident belongs to no workspace. Routing, explicitly:
//
//  1. The recipients are the instance admins (isInstanceAdmin's three rules,
//     suspended accounts excluded) — never workspace owners, who may not
//     administer the instance.
//  2. Each admin gets ONE inbox item, in their primary workspace: the oldest
//     workspace they are an OWNER or ADMIN of, else the oldest they belong
//     to. The item is targeted at them alone (target_user_id), so nobody else
//     in that workspace sees it. An admin who belongs to no workspace has no
//     inbox to put it in; they are skipped (logged) and still see the
//     incident under Admin › Backups.
//  3. The item is kind=message with payload.subkind="backup_incident" (the
//     discriminator convention digest and routine_update use — no new inbox
//     kind, so no inbox_items rebuild), source_id
//     "backup_incident:<incident>:<user>". A repeat Upserts the same row: the
//     card shows the current count and message and comes back as unread.
//     Resolving the incident resolves every item it delivered.
//  4. Category system.health: the inbox's external notifier fans the item out
//     to each admin's own channels (email, Slack, Telegram, …) per their
//     category preferences — that is the "also Slack, email" path.

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// SubkindBackupIncident discriminates a backup incident's message item.
const SubkindBackupIncident = notify.SubkindBackupIncident

// instanceAdminRef is one instance admin and the workspace their inbox item
// goes to ("" when they belong to none).
type instanceAdminRef struct {
	ID, Email, Name, Source, WorkspaceID string
}

// listInstanceAdmins returns every instance admin by isInstanceAdmin's rules.
func listInstanceAdmins(ctx context.Context, db *sql.DB) ([]instanceAdminRef, error) {
	// An install nobody has asked about yet names its first admins now, so a
	// backup incident on it still reaches someone.
	if _, err := ensureInstanceAdminBootstrap(ctx, db); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, email, COALESCE(full_name,''), COALESCE(instance_role,''), COALESCE(suspended_at,'') FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	var out []instanceAdminRef
	for rows.Next() {
		var a instanceAdminRef
		var role, suspended string
		if err := rows.Scan(&a.ID, &a.Email, &a.Name, &role, &suspended); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if src := instanceAdminSourceFor(a.Email, role, suspended != ""); src != "" {
			a.Source = src
			out = append(out, a)
		}
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		_ = db.QueryRowContext(ctx, `SELECT m.workspace_id FROM workspace_members m JOIN workspaces w ON w.id = m.workspace_id
			WHERE m.user_id = ? AND w.deleted_at IS NULL
			ORDER BY CASE WHEN m.role IN ('OWNER','ADMIN') THEN 0 ELSE 1 END, w.created_at, w.id LIMIT 1`, out[i].ID).Scan(&out[i].WorkspaceID)
	}
	return out, nil
}

// backupIncidentAlerter is the backup service's Alerter.
type backupIncidentAlerter struct {
	db     *sql.DB
	logger *slog.Logger
}

func backupIncidentLink(inc *backupplan.Incident) string {
	q := url.Values{"tab": {"backups"}, "section": {"overview"}}
	if inc.RunID != nil && *inc.RunID != "" {
		q.Set("section", "history")
		q.Set("run", *inc.RunID)
	}
	return "/admin?" + q.Encode()
}

func backupIncidentSource(incidentID, userID string) string {
	return "backup_incident:" + incidentID + ":" + userID
}

// Raised writes (or refreshes) the incident's item in every instance admin's
// inbox.
func (a backupIncidentAlerter) Raised(ctx context.Context, inc *backupplan.Incident, opened bool, detail string) {
	admins, err := listInstanceAdmins(ctx, a.db)
	if err != nil {
		a.logger.Warn("backup incident: list instance admins", "incident", inc.ID, "error", err)
		return
	}
	body := inc.Message
	if detail != "" {
		body += "\n\n" + detail
	}
	if inc.Count > 1 {
		body += fmt.Sprintf("\n\n%d failures · one incident · since %s", inc.Count, inc.FirstAt)
	}
	priority := "high"
	if inc.Kind == backupplan.IncidentIncomplete || inc.Kind == backupplan.IncidentStale {
		priority = "medium"
	}
	payload := map[string]any{
		"subkind": SubkindBackupIncident, "incident_id": inc.ID, "incident_kind": inc.Kind, "count": inc.Count,
		"first_at": inc.FirstAt, "last_at": inc.LastAt, "view_url": backupIncidentLink(inc),
	}
	if inc.PlanID != nil {
		payload["plan_id"] = *inc.PlanID
		payload["retry"] = map[string]any{"method": "POST", "path": "/api/v1/admin/instance/backups/run", "body": map[string]string{"plan_id": *inc.PlanID}}
	}
	if inc.RunID != nil {
		payload["run_id"] = *inc.RunID
	}
	var ids []string
	for _, ad := range admins {
		if ad.WorkspaceID == "" {
			a.logger.Info("backup incident: instance admin belongs to no workspace, so has no inbox", "user", ad.Email, "incident", inc.ID)
			continue
		}
		item := inbox.Item{
			WorkspaceID: ad.WorkspaceID, Kind: inbox.KindMessage, SourceID: backupIncidentSource(inc.ID, ad.ID),
			TargetUserID: ad.ID, Title: "Backup needs attention", BodyMD: body,
			SenderType: "system", SenderName: "Backups", Priority: priority, Payload: payload,
			Category: notify.CategorySystemHealth, AttentionClass: inbox.AttentionRepair,
		}
		if err := inbox.Upsert(ctx, a.db, a.logger, item); err != nil {
			continue
		}
		ids = append(ids, "ibx_"+ad.WorkspaceID+"_"+inbox.KindMessage+"_"+item.SourceID)
	}
	if err := backupplan.SetIncidentInboxItems(ctx, a.db, inc.ID, ids); err != nil {
		a.logger.Warn("backup incident: record inbox items", "incident", inc.ID, "error", err)
	}
}

// Resolved resolves every inbox item the incident delivered.
func (a backupIncidentAlerter) Resolved(ctx context.Context, inc *backupplan.Incident) {
	if len(inc.InboxItemIDs) == 0 {
		return
	}
	now := tsformat.Format(time.Now())
	args := []any{now, now}
	for _, id := range inc.InboxItemIDs {
		args = append(args, id)
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE inbox_items SET state = 'resolved', resolved_at = COALESCE(resolved_at, ?),
		resolved_action = COALESCE(resolved_action, 'backup_recovered'), updated_at = ?
		WHERE state != 'resolved' AND id IN (?`+strings.Repeat(",?", len(inc.InboxItemIDs)-1)+`)`, args...); err != nil {
		a.logger.Warn("backup incident: resolve inbox items", "incident", inc.ID, "error", err)
	}
}
