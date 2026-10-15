package usermodel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// FeedbackPreferenceOptionsV1 is a closed catalogue of voluntarily stated future
// preferences. Free text remains feedback; no behavioural inference is performed.
var FeedbackPreferenceOptionsV1 = map[string]string{
	"Next time, keep answers concise.":      "prefers concise answers from this agent",
	"Next time, start with the conclusion.": "prefers the conclusion first from this agent",
	"Příště piš stručněji.":                 "prefers concise answers from this agent",
	"Příště nejdřív ukaž závěr.":            "prefers the conclusion first from this agent",
}

type FeedbackPreference struct {
	ID             string `json:"id"`
	Revision       int    `json:"revision"`
	FeedbackID     string `json:"feedback_id"`
	SourceRevision int    `json:"source_revision"`
	ChatID         string `json:"chat_id"`
	MessageID      string `json:"message_id"`
	Reason         string `json:"reason"`
	Value          string `json:"value"`
	Status         string `json:"status"`
	SourceType     string `json:"source_type"`
}

// preferenceAudience binds recall and self-service reads to the live source,
// trusted membership, private chat opener and live agent. Shared-chat readers
// can give feedback, but do not acquire personal memory through that source.
const preferenceAudience = ` EXISTS (SELECT 1 FROM chats c
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=p.user_id AND wm.access_mode='trusted'
 JOIN conversation_messages m ON m.id=p.message_id AND m.session_id=c.id AND m.role='assistant'
 WHERE c.id=p.chat_id AND c.workspace_id=p.workspace_id AND c.agent_id=p.agent_id
 AND c.created_by=p.user_id AND c.visibility='private') `

func preferenceProfile(ctx context.Context, db *sql.DB) (Profile, error) {
	var name string
	err := db.QueryRowContext(ctx, `SELECT value FROM app_settings WHERE key=?`, SettingProfile).Scan(&name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Profile{}, err
	}
	return ResolveProfile(name)
}

// ProcessFeedbackPreferences drains a bounded durable batch. The final write
// compares both revisions and current authority in the same SQL statement, so a
// stale worker cannot resurrect edited, deleted or revoked source data.
func ProcessFeedbackPreferences(ctx context.Context, db *sql.DB) error {
	profile, err := preferenceProfile(ctx, db)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,revision,source_revision,reason FROM agent_user_preferences WHERE status IN ('processing','failed') LIMIT 100`)
	if err != nil {
		return err
	}
	type pending struct {
		id, reason       string
		revision, source int
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.revision, &p.source, &p.reason); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range batch {
		status, value := "not_eligible", ""
		if !profile.Writes() {
			status = "disabled"
		} else if v, ok := FeedbackPreferenceOptionsV1[p.reason]; ok {
			facts, _ := Verify(profile, []Turn{{Role: "user", BySubject: true, Content: p.reason}}, []Candidate{{Key: "prefers", Value: v, Quote: p.reason, Source: string(SourceStated)}})
			if len(facts) == 1 {
				status, value = "saved", facts[0].Value
			}
		}
		_, err = db.ExecContext(ctx, `UPDATE agent_user_preferences AS p SET value=CASE WHEN `+preferenceAudience+` THEN ? ELSE '' END,
   status=CASE WHEN NOT (`+preferenceAudience+`) THEN 'not_eligible'
    WHEN EXISTS(SELECT 1 FROM user_peer_consent c WHERE c.workspace_id=p.workspace_id AND c.user_id=p.user_id AND c.opted_out=1) THEN 'disabled'
    WHEN EXISTS(SELECT 1 FROM agents a WHERE a.id=p.agent_id AND a.memory_enabled=0) THEN 'disabled'
 WHEN EXISTS(SELECT 1 FROM app_settings WHERE key=? AND value NOT IN ('stated-technical','')) THEN 'disabled' ELSE ? END,
   updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
   WHERE id=? AND revision=? AND source_revision=? AND status IN ('processing','failed')
   AND EXISTS(SELECT 1 FROM message_feedback f WHERE f.id=p.feedback_id AND f.revision=p.source_revision AND COALESCE(f.reason,'')=p.reason)`, value, SettingProfile, status, p.id, p.revision, p.source)
		if err != nil {
			_, _ = db.ExecContext(ctx, `UPDATE agent_user_preferences SET status='failed' WHERE id=? AND revision=? AND source_revision=? AND status IN ('processing','failed')`, p.id, p.revision, p.source)
			return err
		}
	}
	return nil
}

func StartFeedbackPreferenceWorker(db *sql.DB, logger *slog.Logger, stop <-chan struct{}, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := ProcessFeedbackPreferences(ctx, db); err != nil {
				logger.Warn("feedback preference processing unavailable", "error", err)
			}
			cancel()
			select {
			case <-stop:
				return
			case <-timer.C:
			}
		}
	}()
}

func ListFeedbackPreferences(ctx context.Context, db *sql.DB, workspaceID, agentID, userID string) ([]FeedbackPreference, error) {
	out := []FeedbackPreference{}
	if _, err := preferenceProfile(ctx, db); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,revision,feedback_id,source_revision,chat_id,message_id,reason,value,
 CASE WHEN p.status IN ('saved','processing') AND (
 EXISTS(SELECT 1 FROM user_peer_consent c WHERE c.workspace_id=p.workspace_id AND c.user_id=p.user_id AND c.opted_out=1)
 OR EXISTS(SELECT 1 FROM agents a WHERE a.id=p.agent_id AND a.memory_enabled=0)
 OR EXISTS(SELECT 1 FROM app_settings WHERE key='memory.user_model_profile' AND value NOT IN ('stated-technical','')))
 THEN 'disabled' ELSE p.status END
 FROM agent_user_preferences p WHERE workspace_id=? AND agent_id=? AND user_id=? AND status NOT IN ('feedback_only','withdrawn') AND `+preferenceAudience+` ORDER BY updated_at DESC LIMIT 100`, workspaceID, agentID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p FeedbackPreference
		if err = rows.Scan(&p.ID, &p.Revision, &p.FeedbackID, &p.SourceRevision, &p.ChatID, &p.MessageID, &p.Reason, &p.Value, &p.Status); err != nil {
			return nil, err
		}
		p.SourceType = "feedback_reason"
		out = append(out, p)
	}
	return out, rows.Err()
}

func ReadFeedbackPreferences(ctx context.Context, db *sql.DB, workspaceID, agentID, userID string) (string, error) {
	entries, err := ListFeedbackPreferences(ctx, db, workspaceID, agentID, userID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	seen := map[string]bool{}
	for _, p := range entries {
		if p.Status == "saved" && !seen[p.Value] {
			seen[p.Value] = true
			line := fmt.Sprintf("- %s (source: feedback %s revision %d)\n", p.Value, p.FeedbackID, p.SourceRevision)
			if b.Len()+len(line) > 1500 {
				break
			}
			b.WriteString(line)
		}
	}
	return b.String(), nil
}
