package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

func includeConversationUsers(ctx context.Context, tx *sql.Tx, w, where string, args []any) (string, []any, error) {
	exists, err := tableExists(ctx, tx, "workspace_conversations")
	if err != nil || !exists {
		return where, args, err
	}
	where += ` OR id IN (SELECT created_by FROM workspace_conversations WHERE workspace_id=?)`
	args = append(args, w)
	for _, ref := range []struct{ table, column string }{{"workspace_conversation_direct_pairs", "user_low_id"}, {"workspace_conversation_direct_pairs", "user_high_id"}, {"workspace_conversation_members", "user_id"}, {"workspace_conversation_messages", "author_user_id"}, {"workspace_conversation_agents", "joined_by"}, {"workspace_conversation_agent_jobs", "requested_by_user_id"}} {
		exists, err := tableExists(ctx, tx, ref.table)
		if err != nil {
			return "", nil, err
		}
		if !exists {
			continue
		}
		where += fmt.Sprintf(` OR id IN (SELECT %s FROM %s WHERE conversation_id IN (SELECT id FROM workspace_conversations WHERE workspace_id=?))`, quoteIdent(ref.column), quoteIdent(ref.table))
		args = append(args, w)
	}
	return where, args, nil
}

// Restoring a backup never reauthorizes paid work or redelivers historical
// notifications. Keep the durable records and explicit suspension reason. A
// future new message creates fresh jobs/outbox events normally. Existing target
// rows are not altered by INSERT OR IGNORE, nor is the caller's dump mutated.
func safeConversationRestoreRow(table string, row map[string]any, assignments map[string]bool) map[string]any {
	changes := map[string]any{}
	stamp := tsformat.Format(time.Now())
	switch table {
	case "workspace_conversation_activity":
		changes["issues"] = 0
		changes["routines"] = 0
		changes["issues_cursor"] = 0
		changes["routines_cursor"] = 0
	case "workspace_conversation_agent_jobs":
		if row["state"] == "pending" || row["state"] == "queued" {
			changes["state"] = "failed"
			changes["error"] = "Agent work suspended by backup restore; send a new request to run again"
			changes["updated_at"] = stamp
		}
	case "workspace_conversation_outbox":
		if row["delivered_at"] == nil || row["delivered_at"] == "" {
			changes["delivered_at"] = stamp
		}
	case "assignments":
		if row["status"] != "PENDING" && row["status"] != "QUEUED" && row["status"] != "RUNNING" {
			break
		}
		if id, _ := row["id"].(string); assignments[id] {
			changes["status"] = "CANCELLED"
			changes["cancel_requested_at"] = stamp
			changes["finished_at"] = stamp
			changes["error_message"] = "Channel agent work suspended by backup restore"
		}
	}
	if len(changes) == 0 {
		return row
	}
	copyRow := make(map[string]any, len(row)+len(changes))
	for k, v := range row {
		copyRow[k] = v
	}
	for k, v := range changes {
		copyRow[k] = v
	}
	return copyRow
}

func remapConversationMetadata(dump *DBDump, idMap map[string]map[string]string) error {
	if err := canonicalizeDirectPairs(dump); err != nil {
		return err
	}
	if err := remapContinuationRequests(dump, idMap); err != nil {
		return err
	}
	for _, row := range dump.Tables["workspace_conversation_messages"] {
		// Generated activity links identify issues by stable identifier and
		// routines by stable slug; only their workspace query changes on fork.
		// User-authored markdown is never rewritten.
		sourceKind, _ := row["source_kind"].(string)
		if sourceKind == "activity" {
			content, _ := row["content"].(string)
			for oldID, newID := range idMap["workspaces"] {
				content = strings.ReplaceAll(content, "workspace_id="+url.QueryEscape(oldID)+")", "workspace_id="+url.QueryEscape(newID)+")")
			}
			row["content"] = content
		}
		raw, _ := row["mentioned_agent_ids_json"].(string)
		if raw == "" {
			continue
		}
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return fmt.Errorf("backup: invalid conversation mention IDs: %w", err)
		}
		for i, id := range ids {
			if mapped := idMap["agents"][id]; mapped != "" {
				ids[i] = mapped
			}
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return err
		}
		row["mentioned_agent_ids_json"] = string(encoded)
	}
	for _, row := range dump.Tables["inbox_items"] {
		if row["kind"] != "message" {
			continue
		}
		raw, _ := row["payload_json"].(string)
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		old, _ := payload["conversation_id"].(string)
		mapped := idMap["workspace_conversations"][old]
		if mapped == "" {
			continue
		}
		user, _ := row["target_user_id"].(string)
		row["source_id"] = "conversation_" + mapped + "_" + user
		payload["conversation_id"] = mapped
		payload["chat_url"] = groupchat.URL(mapped)
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		row["payload_json"] = string(encoded)
	}
	return nil
}

// User identity reconciliation can reverse the lexical order of a direct pair.
// Recanonicalize after every remap and immediately before restore insertion.
func canonicalizeDirectPairs(dump *DBDump) error {
	if dump == nil {
		return nil
	}
	for _, row := range dump.Tables["workspace_conversation_direct_pairs"] {
		low, _ := row["user_low_id"].(string)
		high, _ := row["user_high_id"].(string)
		if low == "" || high == "" || low == high {
			return fmt.Errorf("backup: invalid direct conversation participants")
		}
		if low > high {
			row["user_low_id"], row["user_high_id"] = high, low
		}
	}
	return nil
}

// Conversation inbox aggregation keys contain the recipient identity as well
// as the conversation ID. FK reconciliation alone cannot rewrite that opaque
// source_id; leaving it behind would create a second aggregate on the next send.
func reconcileConversationInboxSources(dump *DBDump) {
	workspaces := map[string]string{}
	for _, row := range dump.Tables["workspace_conversations"] {
		id, _ := row["id"].(string)
		workspace, _ := row["workspace_id"].(string)
		if id != "" && workspace != "" {
			workspaces[id] = workspace
		}
	}
	for _, row := range dump.Tables["inbox_items"] {
		if row["kind"] != "message" {
			continue
		}
		raw, _ := row["payload_json"].(string)
		var payload map[string]any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			continue
		}
		id, _ := payload["conversation_id"].(string)
		user, _ := row["target_user_id"].(string)
		workspace, _ := row["workspace_id"].(string)
		if user == "" || workspace == "" || workspaces[id] != workspace {
			continue
		}
		row["source_id"] = "conversation_" + id + "_" + user
	}
}

func remapContinuationRequests(dump *DBDump, idMap map[string]map[string]string) error {
	for _, row := range dump.Tables["workspace_conversation_continuations"] {
		raw, _ := row["request_json"].(string)
		var request groupchat.ContinueInput
		if err := json.Unmarshal([]byte(raw), &request); err != nil {
			return fmt.Errorf("backup: invalid continuation retry request: %w", err)
		}
		for i, id := range request.MemberIDs {
			if mapped := idMap["users"][id]; mapped != "" {
				request.MemberIDs[i] = mapped
			}
		}
		sort.Strings(request.MemberIDs)
		if mapped := idMap["agents"][request.AgentID]; mapped != "" {
			request.AgentID = mapped
		}
		encoded, err := json.Marshal(request)
		if err != nil {
			return err
		}
		row["request_json"] = string(encoded)
	}
	return nil
}
