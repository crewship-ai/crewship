package api

import "encoding/json"

// Public Docs wire contracts mirror the versioned managed engine projection.
// Binary Yjs state is authoritative; these structs do not grant write authority.
type DocumentAvailability struct {
	Enabled bool `json:"enabled"`
}
type DocumentNode struct {
	Type    string              `json:"type"`
	Attrs   *DocumentAttributes `json:"attrs,omitempty"`
	Content []DocumentNode      `json:"content,omitempty"`
	Text    string              `json:"text,omitempty"`
	Marks   []DocumentMark      `json:"marks,omitempty"`
}
type DocumentMark struct {
	Type  string                  `json:"type"`
	Attrs *DocumentMarkAttributes `json:"attrs,omitempty"`
}
type DocumentMarkAttributes struct {
	Href *string `json:"href,omitempty"`
}
type DocumentAttributes struct {
	ID       string  `json:"id,omitempty"`
	Level    *int    `json:"level,omitempty"`
	Start    *int    `json:"start,omitempty"`
	Type     *string `json:"type,omitempty"`
	Colspan  *int    `json:"colspan,omitempty"`
	Rowspan  *int    `json:"rowspan,omitempty"`
	Colwidth []int   `json:"colwidth,omitempty"`
	Align    *string `json:"align,omitempty"`
}
type DocumentOutlineBlock struct {
	ID     string  `json:"id"`
	Type   string  `json:"type"`
	Parent *string `json:"parent"`
	Hash   string  `json:"hash"`
	Text   string  `json:"text"`
}
type DocumentBlockHash struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
}
type DocumentStateResponse struct {
	PageID      string                 `json:"page_id"`
	Schema      int                    `json:"schema"`
	Rev         int64                  `json:"rev"`
	Epoch       string                 `json:"epoch"`
	Generation  int64                  `json:"generation"`
	Incarnation string                 `json:"incarnation"`
	Snapshot    string                 `json:"snapshot"`
	SnapshotRev int64                  `json:"snapshotRev"`
	Updates     []string               `json:"updates"`
	Blocks      []DocumentBlockHash    `json:"blocks"`
	Content     DocumentNode           `json:"content"`
	Outline     []DocumentOutlineBlock `json:"outline"`
	CanWrite    bool                   `json:"can_write"`
	CanComment  bool                   `json:"can_comment"`
	CanResolve  bool                   `json:"can_resolve"`
}
type DocumentMutationRequest struct {
	ID           string        `json:"id"`
	Schema       int           `json:"schema"`
	PageID       string        `json:"page_id,omitempty"`
	TargetSchema *int          `json:"target_schema,omitempty"`
	Epoch        string        `json:"epoch"`
	Kind         string        `json:"kind,omitempty"`
	Update       string        `json:"update,omitempty"`
	Target       string        `json:"target,omitempty"`
	Text         string        `json:"text,omitempty"`
	IfRev        *int64        `json:"ifRev,omitempty"`
	IfHash       string        `json:"ifHash,omitempty"`
	Checkpoint   string        `json:"checkpoint,omitempty"`
	Block        string        `json:"block,omitempty"`
	From         *int          `json:"from,omitempty"`
	To           *int          `json:"to,omitempty"`
	Comment      string        `json:"comment,omitempty"`
	Version      *int64        `json:"version,omitempty"`
	Resolved     *bool         `json:"resolved,omitempty"`
	Body         *DocumentNode `json:"body,omitempty"`
}
type DocumentMutationResponse struct {
	ID         string `json:"id"`
	Rev        int64  `json:"rev"`
	Epoch      string `json:"epoch"`
	Actor      string `json:"actor,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Schema     int    `json:"schema,omitempty"`
	Checkpoint string `json:"checkpoint,omitempty"`
	Duplicate  bool   `json:"duplicate,omitempty"`
	Found      bool   `json:"found,omitempty"`
}
type DocumentSelectedTarget struct {
	ID    string `json:"id"`
	Hash  string `json:"hash"`
	From  int    `json:"from"`
	To    int    `json:"to"`
	Quote string `json:"quote"`
}
type documentVerifiedTarget struct {
	DocumentSelectedTarget
	Text      string   `json:"text"`
	Ancestors []string `json:"ancestors"`
	Schema    int      `json:"schema,omitempty"`
}
type DocumentRequestInput struct {
	PageID       string                   `json:"page_id"`
	Targets      []DocumentSelectedTarget `json:"targets"`
	ChatID       string                   `json:"chat_id"`
	Instruction  string                   `json:"instruction"`
	BaseRev      *int64                   `json:"base_rev"`
	SubmissionID string                   `json:"submission_id"`
}
type DocumentRequestResult struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	RunID          string `json:"run_id"`
	ResultRevision int64  `json:"result_revision"`
	CheckpointID   string `json:"checkpoint_id"`
	Error          string `json:"error"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	Instruction    string `json:"instruction"`
	BaseRev        int64  `json:"base_rev"`
}

// DocumentInitializeResponse is the exact-retry initialization receipt. The
// first initialization returns DocumentInitializationState instead. Both use a
// numeric durable fence; subsequent editor operations return an opaque epoch.
type DocumentInitializeResponse struct {
	ID        string `json:"id"`
	Rev       int64  `json:"rev"`
	Epoch     int64  `json:"epoch"`
	Actor     string `json:"actor"`
	Kind      string `json:"kind"`
	Found     bool   `json:"found"`
	Duplicate bool   `json:"duplicate"`
}

type DocumentRequestTarget struct {
	ChatID    string `json:"chat_id"`
	Title     string `json:"title"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	AgentSlug string `json:"agent_slug"`
}
type DocumentRequestListResponse struct {
	Requests      []DocumentRequestResult `json:"requests"`
	CanDispatch   bool                    `json:"can_dispatch"`
	EligibleChats []DocumentRequestTarget `json:"eligible_chats"`
}
type DocumentRequestCreateResponse struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
}

// DocumentInitializationState is the first durable initialize response and the
// private authority's state wire. Public read adds content and an opaque epoch.
type DocumentInitializationState struct {
	Schema      int             `json:"schema"`
	Rev         int64           `json:"rev"`
	Epoch       int64           `json:"epoch"`
	Generation  int64           `json:"generation"`
	Incarnation string          `json:"incarnation"`
	Snapshot    []byte          `json:"snapshot"`
	SnapshotRev int64           `json:"snapshotRev"`
	Updates     [][]byte        `json:"updates"`
	Blocks      json.RawMessage `json:"blocks"`
}

// Checkpoint list rows and checkpoint-read share one public component. Their
// detail-only and list-only fields are optional; identity/schema are common.
type DocumentCheckpointResponse struct {
	ID        string                 `json:"id"`
	Rev       int64                  `json:"rev"`
	Schema    int                    `json:"schema"`
	Actor     string                 `json:"actor,omitempty"`
	CreatedAt string                 `json:"created_at,omitempty"`
	Snapshot  string                 `json:"snapshot,omitempty"`
	Blocks    []DocumentBlockHash    `json:"blocks,omitempty"`
	Content   *DocumentNode          `json:"content,omitempty"`
	Outline   []DocumentOutlineBlock `json:"outline,omitempty"`
}
type DocumentCheckpointsResponse struct {
	Checkpoints []DocumentCheckpointResponse `json:"checkpoints"`
}
type DocumentCommentAnchor struct {
	ID         string `json:"id"`
	Block      string `json:"block"`
	Quote      string `json:"quote"`
	Checkpoint int64  `json:"checkpoint"`
	Start      string `json:"start"`
	End        string `json:"end"`
}
type DocumentCommentResponse struct {
	ID         string                `json:"id"`
	Body       string                `json:"body"`
	Anchor     DocumentCommentAnchor `json:"anchor"`
	Actor      string                `json:"actor"`
	Rev        int64                 `json:"rev"`
	Version    int64                 `json:"version"`
	Resolved   bool                  `json:"resolved"`
	CreatedAt  string                `json:"created_at"`
	Block      string                `json:"block"`
	Quote      string                `json:"quote"`
	Checkpoint int64                 `json:"checkpoint"`
	Start      string                `json:"start"`
	End        string                `json:"end"`
	Status     string                `json:"status"`
	From       *int                  `json:"from,omitempty"`
	To         *int                  `json:"to,omitempty"`
}
type DocumentCommentsResponse struct {
	Comments   []DocumentCommentResponse `json:"comments"`
	CanWrite   bool                      `json:"can_write"`
	CanComment bool                      `json:"can_comment"`
	CanResolve bool                      `json:"can_resolve"`
}
