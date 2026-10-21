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
