package api

import "encoding/json"

// DocumentStructureSelection is a separate explicit human authorization. An
// existing text selection is never rounded up or converted into this scope.
type DocumentStructureSelection struct {
	BlockIDs         []string `json:"block_ids"`
	AllowInsertAfter bool     `json:"allow_insert_after"`
}

type documentStructureScope struct {
	Version          int                 `json:"version"`
	Blocks           []DocumentBlockHash `json:"blocks"`
	Before           *DocumentBlockHash  `json:"before"`
	After            *DocumentBlockHash  `json:"after"`
	AllowInsertAfter bool                `json:"allow_insert_after"`
}

// Public intent contains no client-computed proof/hashes or replacement CRDTs.
// The matched engine computes the progressive operation hashes internally.
type DocumentStructureOperation struct {
	Op       string        `json:"op"`
	ID       string        `json:"id,omitempty"`
	AfterID  string        `json:"after_id,omitempty"`
	BeforeID string        `json:"before_id,omitempty"`
	Node     *DocumentNode `json:"node,omitempty"`
}

type documentStructureOperationProof struct {
	Op         string          `json:"op"`
	ID         string          `json:"id,omitempty"`
	AfterID    string          `json:"after_id,omitempty"`
	BeforeID   string          `json:"before_id,omitempty"`
	IfHash     string          `json:"if_hash,omitempty"`
	AnchorHash string          `json:"anchor_hash,omitempty"`
	Node       json.RawMessage `json:"node,omitempty"`
}

type documentStructuralProof struct {
	RequestID  string                            `json:"request_id"`
	Operations []documentStructureOperationProof `json:"operations"`
	Before     json.RawMessage                   `json:"before"`
	After      json.RawMessage                   `json:"after"`
}
