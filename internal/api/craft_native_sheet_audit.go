package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// auditActionCraftSheetSaved records one saved revision of a native Sheet,
// auditActionCraftDocumentSaved one of a native Document (the same row).
const (
	auditActionCraftSheetSaved    = "artifact.sheet_saved"
	auditActionCraftDocumentSaved = "artifact.document_saved"
)

// Sources of a saved Sheet revision.
const (
	craftSheetSavedByAgent  = "agent"
	craftSheetSavedByEditor = "editor"
	// craftSheetSavedByKeepLocal: the person kept their own work over an agent version they did
	// not replay (X-Craft-Rebase: keep-local on a save over the head).
	craftSheetSavedByKeepLocal = "keep_local"
	// craftSheetSavedByUndo: the person undid an agent change (craft/document/undo).
	craftSheetSavedByUndo       = "undo"
	craftSheetSavedByRestore    = "restore"
	craftSheetSavedByImport     = "import"
	craftSheetSavedByInitialize = "initialize"
	// a person accepted an agent's proposal: the agent and the proposal's request
	// (a proposal does not keep its run)
	craftSheetSavedByAcceptedProposal = "agent_proposal_accepted"
)

// craftSheetSaved is what the audit row says about one saved revision: who,
// which revision, from where, and the size and digest prefix of the saved
// document. Never its content.
type craftSheetSaved struct {
	WorkspaceID string
	PageID      string
	Version     int64
	Source      string
	UserID      string // the person the save acts for; "" for none
	AgentID     string // an agent's save only
	RunID       string // an agent's save only
	RequestID   string // an accepted proposal only
	Application string // "wordcraft" is audited as a Document; anything else as a Sheet
	Native      []byte // the saved document: only its size and digest are kept
}

// recordCraftSheetSaved writes the audit row on the saving transaction (the
// VersionRequest.Record hook), so the revision and its audit commit together
// or not at all. A second row for the same revision is an error, never
// silently dropped.
func recordCraftSheetSaved(ctx context.Context, tx *sql.Tx, s craftSheetSaved) error {
	switch s.Source {
	case craftSheetSavedByAgent:
		if s.AgentID == "" || s.RunID == "" || s.RequestID != "" {
			return errors.New("sheet audit: incomplete record")
		}
	case craftSheetSavedByAcceptedProposal:
		if s.AgentID == "" || s.RequestID == "" || s.RunID != "" {
			return errors.New("sheet audit: incomplete record")
		}
	case craftSheetSavedByEditor, craftSheetSavedByKeepLocal, craftSheetSavedByUndo, craftSheetSavedByRestore, craftSheetSavedByImport, craftSheetSavedByInitialize:
		if s.AgentID != "" || s.RunID != "" || s.RequestID != "" {
			return errors.New("sheet audit: incomplete record")
		}
	default:
		return errors.New("sheet audit: unknown source")
	}
	if tx == nil || s.WorkspaceID == "" || s.PageID == "" || s.Version <= 0 || len(s.Native) == 0 {
		return errors.New("sheet audit: incomplete record")
	}
	sum := sha256.Sum256(s.Native)
	fields := map[string]any{
		"version": strconv.FormatInt(s.Version, 10),
		"source":  s.Source,
		"bytes":   len(s.Native),
		"sha256":  hex.EncodeToString(sum[:8]),
	}
	if s.AgentID != "" {
		fields["agent_id"] = s.AgentID
	}
	if s.RunID != "" {
		fields["run_id"] = s.RunID
	}
	if s.RequestID != "" {
		fields["request_id"] = s.RequestID
	}
	action, prefix := auditActionCraftSheetSaved, "sheet-saved-"
	if s.Application == "wordcraft" {
		action, prefix = auditActionCraftDocumentSaved, "document-saved-"
	}
	meta, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	var user any
	if s.UserID != "" {
		user = s.UserID
	}
	key := sha256.Sum256([]byte(s.PageID + "\x00" + strconv.FormatInt(s.Version, 10)))
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,'page',?,?,?)`,
		prefix+hex.EncodeToString(key[:16]), s.WorkspaceID, user, action, s.PageID, string(meta), tsformat.Format(time.Now().UTC()))
	return err
}
