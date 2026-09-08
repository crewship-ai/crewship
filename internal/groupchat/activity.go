package groupchat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/statuses"
	"net/url"
	"strings"
)

// ActivitySettings opts a workspace-visible channel into future lifecycle
// events. Private conversation history never becomes workspace activity.
type ActivitySettings struct {
	Issues   bool `json:"issues"`
	Routines bool `json:"routines"`
}

func activitySettings(ctx context.Context, q querier, id string) (out ActivitySettings, err error) {
	err = q.QueryRowContext(ctx, `SELECT issues,routines FROM workspace_conversation_activity WHERE conversation_id=?`, id).Scan(&out.Issues, &out.Routines)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return
}
func (s *Store) Activity(ctx context.Context, w, u, id string) (ActivitySettings, error) {
	c, err := get(ctx, s.db, w, u, id)
	if err != nil {
		return ActivitySettings{}, err
	}
	if c.Kind != "channel" {
		return ActivitySettings{}, ErrInvalid
	}
	return activitySettings(ctx, s.db, id)
}
func (s *Store) SetActivity(ctx context.Context, w, u, id string, in ActivitySettings) error {
	return s.write(ctx, func(q querier) error {
		c, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		if c.Kind != "channel" {
			return ErrInvalid
		}
		if c.CreatedBy != u {
			return ErrForbidden
		}
		var head int64
		if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM journal_entries WHERE workspace_id=? AND seq>0`, w).Scan(&head); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_activity(conversation_id,issues,routines,issues_cursor,routines_cursor) VALUES(?,?,?,?,?) ON CONFLICT(conversation_id) DO UPDATE SET issues=excluded.issues,routines=excluded.routines,issues_cursor=CASE WHEN excluded.issues=1 AND workspace_conversation_activity.issues=0 THEN excluded.issues_cursor ELSE workspace_conversation_activity.issues_cursor END,routines_cursor=CASE WHEN excluded.routines=1 AND workspace_conversation_activity.routines=0 THEN excluded.routines_cursor ELSE workspace_conversation_activity.routines_cursor END`, id, in.Issues, in.Routines, head, head)
		return err
	})
}

// ProjectActivity drains bounded batches per opted-in channel. The journal
// cursor, messages and notification outbox commit together, so replay after a
// crash cannot duplicate cards or silently lose an accepted projection. This
// path does not invoke Send, create agent jobs, parse mentions or call models.
func (s *Store) ProjectActivity(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT a.conversation_id FROM workspace_conversation_activity a JOIN workspace_conversations c ON c.id=a.conversation_id JOIN workspaces w ON w.id=c.workspace_id WHERE (a.issues=1 OR a.routines=1) AND c.kind='channel' AND c.deleted_at IS NULL AND w.deleted_at IS NULL`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.projectChannelActivity(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) projectChannelActivity(ctx context.Context, id string) error {
	return s.write(ctx, func(q querier) error {
		var w string
		var sequence, issuesCursor, routinesCursor int64
		var issues, routines bool
		err := q.QueryRowContext(ctx, `SELECT c.workspace_id,c.last_sequence,a.issues,a.routines,a.issues_cursor,a.routines_cursor FROM workspace_conversation_activity a JOIN workspace_conversations c ON c.id=a.conversation_id JOIN workspaces w ON w.id=c.workspace_id WHERE c.id=? AND c.kind='channel' AND c.deleted_at IS NULL AND w.deleted_at IS NULL`, id).Scan(&w, &sequence, &issues, &routines, &issuesCursor, &routinesCursor)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		type event struct {
			seq                    int64
			kind, mission, payload string
		}
		var events []event
		rows, err := q.QueryContext(ctx, `SELECT seq,entry_type,COALESCE(mission_id,''),payload FROM journal_entries WHERE workspace_id=? AND seq>0 AND ((? AND seq>? AND entry_type IN ('mission.created','mission.status_change','mission.assigned')) OR (? AND seq>? AND entry_type IN ('pipeline.run.started','pipeline.run.completed','pipeline.run.failed'))) ORDER BY seq LIMIT 50`, w, issues, issuesCursor, routines, routinesCursor)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e event
			if err = rows.Scan(&e.seq, &e.kind, &e.mission, &e.payload); err != nil {
				rows.Close()
				return err
			}
			events = append(events, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range events {
			content, err := activityContent(ctx, q, w, e.kind, e.mission, e.payload)
			if err != nil {
				return err
			}
			if strings.HasPrefix(e.kind, "mission.") {
				issuesCursor = e.seq
			} else {
				routinesCursor = e.seq
			}
			if content == "" {
				continue
			}
			sequence++
			mid, stamp := newID(), now()
			if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_messages(id,conversation_id,sequence,client_id,content,created_at,source_kind) VALUES(?,?,?,?,?,?,'activity')`, mid, id, sequence, fmt.Sprintf("activity:%d", e.seq), content, stamp); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_outbox(id,conversation_id,message_id,created_at) VALUES(?,?,?,?)`, newID(), id, mid, stamp); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE workspace_conversations SET last_sequence=?,updated_at=? WHERE id=?`, sequence, stamp, id); err != nil {
				return err
			}
		}
		// Advance past unrelated journal traffic too; otherwise a quiet family
		// would rescan the same telemetry forever. A full batch retains its
		// last event cursor so the next drain catches the remaining events.
		if len(events) < 50 {
			var head int64
			if err = q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM journal_entries WHERE workspace_id=? AND seq>0`, w).Scan(&head); err != nil {
				return err
			}
			if issues && head > issuesCursor {
				issuesCursor = head
			}
			if routines && head > routinesCursor {
				routinesCursor = head
			}
		}
		_, err = q.ExecContext(ctx, `UPDATE workspace_conversation_activity SET issues_cursor=?,routines_cursor=? WHERE conversation_id=?`, issuesCursor, routinesCursor, id)
		return err
	})
}

// Resolve resource identities inside the event's workspace. Journal summaries,
// comments, run outputs and arbitrary payload strings are deliberately excluded.
func activityContent(ctx context.Context, q querier, w, kind, mission, payload string) (string, error) {
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return "", nil
	}
	if strings.HasPrefix(kind, "mission.") {
		var identifier, title string
		err := q.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(m.identifier,''),m.id),m.title FROM missions m JOIN crews c ON c.id=m.crew_id AND c.workspace_id=m.workspace_id WHERE m.id=? AND m.workspace_id=? AND c.deleted_at IS NULL`, mission, w).Scan(&identifier, &title)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		label := map[string]string{"mission.created": "Issue created", "mission.status_change": "Issue updated", "mission.assigned": "Issue assignment changed"}[kind]
		if kind == "mission.status_change" {
			if status, ok := p["to"].(string); ok {
				if _, known := statuses.ValidIssueTransitions[status]; known {
					label += " · " + strings.ReplaceAll(status, "_", " ")
				}
			}
		}
		return fmt.Sprintf("%s · [%s](%s)", label, activityLabel(identifier+" · "+title), "/issues/"+url.PathEscape(identifier)+"?workspace_id="+url.QueryEscape(w)), nil
	}
	pipeline, _ := p["pipeline_id"].(string)
	var slug, name string
	err := q.QueryRowContext(ctx, `SELECT slug,name FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL AND workspace_visible=1 AND ephemeral=0`, pipeline, w).Scan(&slug, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	label := map[string]string{"pipeline.run.started": "Routine run started", "pipeline.run.completed": "Routine run completed", "pipeline.run.failed": "Routine run failed"}[kind]
	return fmt.Sprintf("%s · [%s](%s)", label, activityLabel(slug+" · "+name), "/routines?slug="+url.QueryEscape(slug)+"&workspace_id="+url.QueryEscape(w)), nil
}

// Labels are resource identities, escaped so punctuation cannot inject markdown.
func activityLabel(s string) string {
	if runes := []rune(s); len(runes) > 200 {
		s = string(runes[:199]) + "…"
	}
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`", "\n", " ", "\r", " ", "<", "&lt;", ">", "&gt;").Replace(s)
}
