package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func (r *Router) readProposal(ctx context.Context, a chataudience.Receipt, id string) (ChatCardProjection, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ChatCardProjection{}, err
	}
	defer tx.Rollback()
	if err = proposalHuman(ctx, tx, a, false); err != nil {
		return ChatCardProjection{}, err
	}
	p, err := loadProposal(ctx, tx, a.ChatID, id)
	if err != nil {
		return ChatCardProjection{}, err
	}
	if p.Workspace != a.WorkspaceID {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	if err = proposalItemsVisible(ctx, tx, p); err != nil {
		return ChatCardProjection{}, err
	}
	if p.Card.Status == "draft" && proposalHuman(ctx, tx, a, true) == nil {
		p.Card.AllowedActions = []string{"view", "edit", "confirm", "discard"}
	}
	return ChatCardProjection{ChatID: p.Chat, Ref: p.response()}, nil
}

func (r *Router) chatProposalShow(w http.ResponseWriter, req *http.Request) {
	r.ChatCardProjectionHandler(r.readProposal)(w, req)
}

func (r *Router) chatProposals(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a, err := r.entityRefAudience(req, req.PathValue("chatId"))
	if err != nil {
		proposalError(w, err)
		return
	}
	// Reuse authenticated opaque replacement cursors; internal scan positions and
	// hidden source counts never leave the server.
	c := entityCursor{Audience: *a, Snapshot: generateCUID(), Expires: time.Now().Add(10 * time.Minute).Unix()}
	reset := true
	if raw := req.URL.Query().Get("cursor"); raw != "" {
		c, err = r.decodeProposalCursor(raw)
		if err != nil || c.Audience != *a {
			replyError(w, 400, "Invalid cursor")
			return
		}
		reset = c.Complete
		if reset {
			c = entityCursor{Audience: *a, Snapshot: generateCUID(), Expires: time.Now().Add(10 * time.Minute).Unix()}
		}
	}
	tx, err := r.db.BeginTx(req.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		proposalError(w, err)
		return
	}
	defer tx.Rollback()
	if err = proposalHuman(req.Context(), tx, *a, false); err != nil {
		proposalError(w, err)
		return
	}
	if reset {
		if err = tx.QueryRowContext(req.Context(), `SELECT COALESCE(MAX(rowid),0) FROM chat_issue_proposals WHERE chat_id=? AND `+proposalVisibleSQL, a.ChatID).Scan(&c.Upper); err != nil {
			proposalError(w, err)
			return
		}
	}
	rows, err := tx.QueryContext(req.Context(), `SELECT rowid,id FROM chat_issue_proposals WHERE chat_id=? AND rowid>? AND rowid<=? AND `+proposalVisibleSQL+` ORDER BY rowid LIMIT 101`, a.ChatID, c.After, c.Upper)
	if err != nil {
		proposalError(w, err)
		return
	}
	type entry struct {
		seq int64
		id  string
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.seq, &e.id); err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		proposalError(w, err)
		return
	}
	more := len(entries) > 100
	if more {
		entries = entries[:100]
	}
	page := ChatCardPage{ContractVersion: 1, Reset: reset, SnapshotID: c.Snapshot, Refs: []ChatCardResponse{}, HasMore: more, SnapshotComplete: !more}
	for _, e := range entries {
		c.After = e.seq
		p, e2 := loadProposal(req.Context(), tx, a.ChatID, e.id)
		if e2 == nil {
			e2 = proposalItemsVisible(req.Context(), tx, p)
		}
		if errors.Is(e2, sql.ErrNoRows) {
			continue
		}
		if e2 != nil {
			proposalError(w, e2)
			return
		}
		if p.Card.Status == "draft" && proposalHuman(req.Context(), tx, *a, true) == nil {
			p.Card.AllowedActions = []string{"view", "edit", "confirm", "discard"}
		}
		page.Refs = append(page.Refs, p.response())
	}
	c.Complete = !more
	page.NextCursor, err = r.encodeProposalCursor(c)
	if err != nil {
		proposalError(w, err)
		return
	}
	// End the snapshot before rechecking the live chat audience, as E1 does.
	if err = tx.Commit(); err != nil {
		proposalError(w, err)
		return
	}
	current, err := r.entityRefAudience(req, a.ChatID)
	if err != nil || *current != *a {
		proposalError(w, errEntityAudienceDenied)
		return
	}
	writeJSON(w, 200, page)
}

func (r *Router) chatProposalMutate(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a, err := r.entityRefAudience(req, req.PathValue("chatId"))
	if err != nil {
		proposalError(w, err)
		return
	}
	key := req.Header.Get("Idempotency-Key")
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		replyError(w, 400, "Idempotency-Key is required (maximum 200 bytes)")
		return
	}
	id := req.PathValue("sourceId")
	verb := req.PathValue("action")
	if req.Method == http.MethodPatch {
		verb = "edit"
	}
	var body any
	var version int64
	switch verb {
	case "edit":
		var b chatentities.ProposalEdit
		err = readJSON(req, &b)
		body = b
		version = b.ExpectedVersion
	case "confirm":
		var b chatentities.ProposalConfirm
		err = readJSON(req, &b)
		body = b
		version = b.ReviewedVersion
		if b.ProposalID != id {
			err = chatentities.ErrInvalid
		}
	case "discard":
		var b chatentities.ProposalDiscard
		err = readJSON(req, &b)
		body = b
		version = b.ExpectedVersion
	default:
		replyError(w, 404, "Card not found")
		return
	}
	if err != nil || version < 1 || version > maxCardInteger {
		replyError(w, 400, "Invalid proposal command")
		return
	}
	result, err := r.mutateProposal(req, *a, id, key, verb, version, body)
	if errors.Is(err, errProposalVersion) { // Never disclose the conflict snapshot after access changed.
		current, e := r.readProposal(req.Context(), *a, id)
		if e != nil {
			proposalError(w, e)
			return
		}
		writeJSON(w, 409, ChatCardVersionConflict{Error: "proposal_version_conflict", Current: current.Ref})
		return
	}
	if err != nil {
		proposalError(w, err)
		return
	}
	authorized, err := r.readProposal(req.Context(), *a, id)
	if err != nil {
		proposalError(w, err)
		return
	}
	if result.Proposal.Status == "draft" {
		// A concurrent later revision/terminal transition may settle before the
		// response projection. Never replace that source with an older draft.
		if authorized.Ref.Proposal.Version != result.Proposal.Version || authorized.Ref.Proposal.Status != "draft" {
			result = authorized.Ref
		} else {
			result.Proposal.AllowedActions = authorized.Ref.Proposal.AllowedActions
		}
	}
	broadcastProposalInvalidation(req.Context(), r.db, r.hub, a.WorkspaceID, id)
	writeJSON(w, 200, result)
}

func proposalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, errEntityAudienceDenied):
		replyError(w, 404, "Card not found")
	case errors.Is(err, runauthority.ErrUnavailable), errors.Is(err, runauthority.ErrIdentityMismatch), errors.Is(err, runauthority.ErrRequesterStale), errors.Is(err, errProposalForbidden):
		replyError(w, 403, "Proposal action forbidden")
	case errors.Is(err, chatentities.ErrKeyConflict):
		replyError(w, 409, "operation_key_conflict")
	case errors.Is(err, chatentities.ErrInvalid), errors.Is(err, errInvalidCard), errors.Is(err, chatentities.ErrKeyRequired):
		replyError(w, 400, "Invalid proposal command")
	default:
		replyError(w, 500, "Could not process proposal")
	}
}

// Internal-only transport. The sidecar supplies these identities from its
// verified per-run token; the public/agent tool command contains intent only.
func (r *Router) internalProposalCreate(w http.ResponseWriter, req *http.Request) {
	var body struct {
		chatentities.ProposalCreate
		WorkspaceID  string `json:"workspace_id"`
		AgentID      string `json:"agent_id"`
		RunID        string `json:"run_id"`
		OperationKey string `json:"operation_key"`
	}
	if err := readJSON(req, &body); err != nil {
		replyError(w, 400, "Invalid proposal command")
		return
	}
	if !assertInternalTokenWorkspace(w, req, body.WorkspaceID) {
		return
	}
	result, err := r.createProposal(req.Context(), body.RunID, body.AgentID, body.WorkspaceID, body.OperationKey, body.ProposalCreate)
	if err != nil {
		proposalError(w, err)
		return
	}
	broadcastProposalInvalidation(req.Context(), r.db, r.hub, body.WorkspaceID, result.Entity.ID)
	writeJSON(w, 201, result)
}

func (r *Router) chatProposalConfirm(w http.ResponseWriter, req *http.Request) {
	req.SetPathValue("action", "confirm")
	r.chatProposalMutate(w, req)
}
func (r *Router) chatProposalDiscard(w http.ResponseWriter, req *http.Request) {
	req.SetPathValue("action", "discard")
	r.chatProposalMutate(w, req)
}

// Purpose separation makes E1 and proposal cursors cryptographically distinct.
func (r *Router) proposalCursorRouter() (*Router, error) {
	if _, err := r.entityCursorCipher(); err != nil {
		return nil, err
	}
	key := sha256.Sum256(append([]byte("chat-proposal-cursor:v1:"), r.entityCursorKey...))
	scoped := &Router{entityCursorKey: key[:]}
	// E1 initializes keys lazily. The derived source key is already initialized.
	scoped.entityCursorOnce.Do(func() {})
	return scoped, nil
}
func (r *Router) encodeProposalCursor(c entityCursor) (string, error) {
	scoped, err := r.proposalCursorRouter()
	if err != nil {
		return "", err
	}
	return scoped.encodeEntityCursor(c)
}
func (r *Router) decodeProposalCursor(raw string) (entityCursor, error) {
	scoped, err := r.proposalCursorRouter()
	if err != nil {
		return entityCursor{}, err
	}
	return scoped.decodeEntityCursor(raw)
}

// Visibility precedes LIMIT and snapshot upper bound. Hidden rows must not
// affect page length, has_more, or any hint that another source exists.
const proposalVisibleSQL = `NOT EXISTS (
 SELECT 1 FROM json_each(chat_issue_proposals.items) item WHERE
 NOT EXISTS(SELECT 1 FROM crews c WHERE c.id=json_extract(item.value,'$.crew_id') AND c.workspace_id=chat_issue_proposals.workspace_id AND c.deleted_at IS NULL)
 OR (json_extract(item.value,'$.project_id') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM projects p WHERE p.id=json_extract(item.value,'$.project_id') AND p.workspace_id=chat_issue_proposals.workspace_id))
 OR (json_extract(item.value,'$.assignee_id') IS NOT NULL AND (
 (SELECT COUNT(*) FROM workspace_members wm JOIN users u ON u.id=wm.user_id WHERE wm.workspace_id=chat_issue_proposals.workspace_id AND wm.user_id=json_extract(item.value,'$.assignee_id') AND u.suspended_at IS NULL)
 +(SELECT COUNT(*) FROM agents a WHERE a.workspace_id=chat_issue_proposals.workspace_id AND a.id=json_extract(item.value,'$.assignee_id') AND a.deleted_at IS NULL))<>1)
)`

func (r *Router) internalProposalEdit(w http.ResponseWriter, req *http.Request) {
	var body struct {
		chatentities.ProposalEdit
		WorkspaceID  string `json:"workspace_id"`
		AgentID      string `json:"agent_id"`
		RunID        string `json:"run_id"`
		OperationKey string `json:"operation_key"`
	}
	if err := readJSON(req, &body); err != nil {
		replyError(w, 400, "Invalid proposal command")
		return
	}
	if !assertInternalTokenWorkspace(w, req, body.WorkspaceID) {
		return
	}
	result, err := r.editAgentProposal(req.Context(), body.RunID, body.AgentID, body.WorkspaceID, req.PathValue("sourceId"), body.OperationKey, body.ProposalEdit)
	if errors.Is(err, errProposalVersion) {
		writeJSON(w, 409, ChatCardVersionConflict{Error: "proposal_version_conflict", Current: result})
		return
	}
	if err != nil {
		proposalError(w, err)
		return
	}
	broadcastProposalInvalidation(req.Context(), r.db, r.hub, body.WorkspaceID, result.Entity.ID)
	writeJSON(w, 200, result)
}
