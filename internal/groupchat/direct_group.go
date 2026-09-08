package groupchat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// ContinueInput creates another room with fresh history. Only explicit channel
// continuation may join an agent, because its execution is workspace-visible.
type ContinueInput struct {
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	MemberIDs []string `json:"member_ids"`
	AgentID   string   `json:"agent_id,omitempty"`
	ClientID  string   `json:"client_id"`
}

func normalizeContinue(in ContinueInput) (ContinueInput, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" || !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Title) > 120 || len(in.MemberIDs) > 498 || strings.TrimSpace(in.ClientID) == "" || len(in.ClientID) > 128 || !utf8.ValidString(in.ClientID) {
		return in, ErrInvalid
	}
	if in.Kind != "group" && in.Kind != "channel" {
		return in, ErrInvalid
	}
	if (in.Kind == "group" && (len(in.MemberIDs) == 0 || in.AgentID != "")) || (in.Kind == "channel" && in.AgentID == "") {
		return in, ErrInvalid
	}
	ids := map[string]bool{}
	for _, id := range in.MemberIDs {
		if strings.TrimSpace(id) == "" {
			return in, ErrInvalid
		}
		ids[id] = true
	}
	in.MemberIDs = make([]string, 0, len(ids))
	for id := range ids {
		in.MemberIDs = append(in.MemberIDs, id)
	}
	sort.Strings(in.MemberIDs)
	return in, nil
}

// Continue starts a new private group from a DM, or an explicit workspace
// channel from a private conversation. Nothing copies or widens source history.
// Durable retry identity and canonical request commit with the whole new room.
func (s *Store) Continue(ctx context.Context, w, u, id string, input ContinueInput) (Conversation, bool, error) {
	in, err := normalizeContinue(input)
	if err != nil {
		return Conversation{}, false, err
	}
	request, _ := json.Marshal(in)
	var room Conversation
	created := false
	err = s.write(ctx, func(q querier) error {
		source, err := getForWrite(ctx, q, w, u, id)
		if err != nil {
			return err
		}
		if source.Kind != "group" || (in.Kind == "group" && !source.IsDirect) {
			return ErrInvalid
		}
		var target, saved string
		err = q.QueryRowContext(ctx, `SELECT target_conversation_id,request_json FROM workspace_conversation_continuations WHERE source_conversation_id=? AND requested_by_user_id=? AND client_id=?`, id, u, in.ClientID).Scan(&target, &saved)
		if err == nil {
			if saved != string(request) {
				return ErrConflict
			}
			room, err = get(ctx, q, w, u, target)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ids := map[string]bool{}
		rows, err := q.QueryContext(ctx, `SELECT user_id FROM workspace_conversation_members WHERE conversation_id=?`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var member string
			if err = rows.Scan(&member); err != nil {
				rows.Close()
				return err
			}
			ids[member] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if source.IsDirect {
			var low, high string
			err = q.QueryRowContext(ctx, `SELECT user_low_id,user_high_id FROM workspace_conversation_direct_pairs WHERE conversation_id=? AND workspace_id=?`, id, w).Scan(&low, &high)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrForbidden
			}
			if err != nil {
				return err
			}
			if len(ids) != 2 || !ids[low] || !ids[high] || (u != low && u != high) {
				return ErrForbidden
			}
		}
		before := len(ids)
		for _, member := range in.MemberIDs {
			ids[member] = true
		}
		if len(ids) > 500 || (in.Kind == "group" && len(ids) == before) {
			return ErrInvalid
		}
		for member := range ids {
			if err = workspaceMember(ctx, q, w, member); err != nil {
				return err
			}
		}
		var agentName string
		if in.Kind == "channel" {
			err = q.QueryRowContext(ctx, `SELECT a.name FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND c.deleted_at IS NULL`, in.AgentID, w).Scan(&agentName)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrForbidden
			}
			if err != nil {
				return err
			}
		}
		stamp := now()
		room = Conversation{ID: newID(), WorkspaceID: w, Kind: in.Kind, Title: in.Title, CreatedBy: u, CreatedAt: stamp, UpdatedAt: stamp, AccessScope: "participants"}
		if in.Kind == "channel" {
			room.AccessScope = "workspace"
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversations(id,workspace_id,kind,title,created_by,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, room.ID, w, in.Kind, in.Title, u, stamp, stamp); err != nil {
			return err
		}
		for member := range ids {
			if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_members(conversation_id,user_id,joined_at) VALUES(?,?,?)`, room.ID, member, stamp); err != nil {
				return err
			}
		}
		if in.Kind == "channel" {
			if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_agents(conversation_id,agent_id,joined_by,joined_at) VALUES(?,?,?,?)`, room.ID, in.AgentID, u, stamp); err != nil {
				return err
			}
			if err = appendEventMessage(ctx, q, Message{ID: newID(), ConversationID: room.ID, Sequence: 1, AuthorUserID: u, ClientID: newID(), Kind: "agent_joined", SubjectAgentID: in.AgentID, Content: fmt.Sprintf("%s joined this workspace channel. This is a new discussion; private history was not copied.", agentName), CreatedAt: stamp}); err != nil {
				return err
			}
			room.LastSequence = 1
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO workspace_conversation_continuations(source_conversation_id,requested_by_user_id,client_id,request_json,target_conversation_id) VALUES(?,?,?,?,?)`, id, u, in.ClientID, string(request), room.ID); err != nil {
			return err
		}
		created = true
		return nil
	})
	return room, created, err
}
