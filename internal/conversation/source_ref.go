package conversation

import (
	"fmt"
	"time"
)

const SourceContractVersion = 1
const SourceProjection = "source_v1"

type SourceKind string

const (
	SourceAgentSession  SourceKind = "agent_session"
	SourceWorkspaceRoom SourceKind = "workspace_room"
)

// ConversationRef names an existing source; it grants no access to that source.
type ConversationRef struct {
	WorkspaceID string     `json:"workspace_id"`
	SourceKind  SourceKind `json:"source_kind"`
	SourceID    string     `json:"source_id"`
}
type MessageRef struct {
	ConversationRef ConversationRef `json:"conversation_ref"`
	SourceMessageID string          `json:"source_message_id"`
}
type SourceAuthor struct {
	UserID  string `json:"user_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}
type SourceOrder struct {
	SourceSequence *int64 `json:"source_sequence,omitempty"`
	// SourcePosition is an ordinal in this bounded snapshot, never an event cursor.
	SourcePosition *int64 `json:"source_position,omitempty"`
}
type SourceMessage struct {
	MessageRef        *MessageRef   `json:"message_ref"`
	Role              *string       `json:"role"`
	Author            *SourceAuthor `json:"author"`
	OccurredAt        *string       `json:"occurred_at"`
	Order             SourceOrder   `json:"order"`
	ReplyTo           *MessageRef   `json:"reply_to"`
	ThreadRoot        *MessageRef   `json:"thread_root"`
	AlternativeParent *MessageRef   `json:"alternative_parent"`
	Content           string        `json:"content"`
	Parts             []Part        `json:"parts"`
}
type SourceMessagesResponse struct {
	ContractVersion  int             `json:"contract_version"`
	ConversationRef  ConversationRef `json:"conversation_ref"`
	Messages         []SourceMessage `json:"messages"`
	SnapshotComplete *bool           `json:"snapshot_complete,omitempty"`
	HasMore          *bool           `json:"has_more,omitempty"`
}

func (r ConversationRef) Validate() error {
	if r.WorkspaceID == "" || validateID(r.SourceID) != nil || (r.SourceKind != SourceAgentSession && r.SourceKind != SourceWorkspaceRoom) {
		return fmt.Errorf("invalid conversation reference")
	}
	return nil
}

// ProjectAgentSnapshot reads no additional source and does not infer legacy identity.
func ProjectAgentSnapshot(ref ConversationRef, rows []Message) (SourceMessagesResponse, error) {
	out := SourceMessagesResponse{ContractVersion: SourceContractVersion, ConversationRef: ref, Messages: make([]SourceMessage, 0, len(rows))}
	if ref.Validate() != nil || ref.SourceKind != SourceAgentSession || len(rows) > 1000 {
		return out, fmt.Errorf("invalid source snapshot")
	}
	seen := map[string]bool{}
	for i, m := range rows {
		if m.ChatID != "" && m.ChatID != ref.SourceID {
			return out, fmt.Errorf("source identity mismatch")
		}
		pos := int64(i)
		item := SourceMessage{Order: SourceOrder{SourcePosition: &pos}, Content: m.Content, Parts: make([]Part, 0, len(m.NormalizedParts()))}
		if m.ID != "" {
			if seen[m.ID] {
				return out, fmt.Errorf("duplicate source identity")
			}
			seen[m.ID] = true
			item.MessageRef = &MessageRef{ConversationRef: ref, SourceMessageID: m.ID}
		}
		if m.Role != "" {
			role := string(m.Role)
			item.Role = &role
		}
		if m.Role == RoleUser && m.AuthorUserID != "" {
			item.Author = &SourceAuthor{UserID: m.AuthorUserID}
		}
		if m.Role == RoleAssistant && m.AgentID != "" {
			item.Author = &SourceAuthor{AgentID: m.AgentID}
		}
		if !m.Timestamp.IsZero() {
			ts := m.Timestamp.UTC().Format(time.RFC3339Nano)
			item.OccurredAt = &ts
		}
		for _, part := range m.NormalizedParts() {
			safe := part
			safe.Metadata = nil
			if safe.Type == "thinking" {
				safe.Content = ""
			}
			item.Parts = append(item.Parts, safe)
		}
		out.Messages = append(out.Messages, item)
	}
	complete := true
	out.SnapshotComplete = &complete
	return out, nil
}
