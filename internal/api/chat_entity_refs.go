package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
)

// EntityRefPage is a bounded replacement snapshot. The first page resets the
// old view; subsequent pages append to the same snapshot. Consumers must not
// reconcile removed entities until SnapshotComplete. Poll using a completed
// cursor starts a fresh replacement snapshot, deliberately avoiding a guessed
// grant fingerprint. No internal sequence or hidden-object count is exposed.
type EntityRefPage struct {
	Reset            bool                `json:"reset"`
	NextCursor       string              `json:"next_cursor"`
	SnapshotID       string              `json:"snapshot_id"`
	SnapshotComplete bool                `json:"snapshot_complete"`
	HasMore          bool                `json:"has_more"`
	Refs             []EntityRefResponse `json:"refs"`
}
type EntityRefResponse struct {
	EventID          string              `json:"event_id"`
	Kind             string              `json:"kind"`
	Verb             string              `json:"verb"`
	Entity           EntityRefEntity     `json:"entity"`
	Provenance       EntityRefProvenance `json:"provenance"`
	Anchor           EntityRefAnchor     `json:"anchor"`
	RevisionAtAction EntityRefRevision   `json:"revision_at_action"`
}
type EntityRefEntity struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	WorkspaceID string `json:"workspace_id"`
	Deleted     bool   `json:"deleted,omitempty"`
}
type EntityRefActor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type EntityRefProvenance struct {
	Actor EntityRefActor `json:"actor"`
	At    string         `json:"at"`
}
type EntityRefAnchor struct {
	TriggerMessageID string  `json:"trigger_message_id"`
	RunID            string  `json:"run_id"`
	RunStatus        *string `json:"run_status"`
}
type EntityRefRevision struct {
	Revision          int64 `json:"revision"`
	AggregateRevision int64 `json:"aggregate_revision"`
}
type entityCursor struct {
	Audience chataudience.Receipt `json:"a"`
	After    int64                `json:"p"`
	Upper    int64                `json:"u"`
	Snapshot string               `json:"s"`
	Complete bool                 `json:"c"`
	Expires  int64                `json:"e"`
}

func (r *Router) entityCursorCipher() (cipher.AEAD, error) {
	r.entityCursorOnce.Do(func() {
		r.entityCursorKey = make([]byte, 32)
		if _, err := rand.Read(r.entityCursorKey); err != nil {
			r.entityCursorKey = nil
		}
	})
	if len(r.entityCursorKey) != 32 {
		return nil, errors.New("cursor entropy unavailable")
	}
	block, err := aes.NewCipher(r.entityCursorKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func (r *Router) encodeEntityCursor(c entityCursor) (string, error) {
	a, err := r.entityCursorCipher()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(a.Seal(nonce, nonce, data, []byte("chat-entity-refs:v1"))), nil
}
func (r *Router) decodeEntityCursor(raw string) (entityCursor, error) {
	var c entityCursor
	if len(raw) > 4096 {
		return c, errors.New("cursor_invalid")
	}
	a, err := r.entityCursorCipher()
	if err != nil {
		return c, err
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) < a.NonceSize() {
		return c, errors.New("cursor_invalid")
	}
	data, err = a.Open(nil, data[:a.NonceSize()], data[a.NonceSize():], []byte("chat-entity-refs:v1"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	if err == nil && (c.Expires < time.Now().Unix() || c.After < 0 || c.Upper < c.After || c.Snapshot == "") {
		err = errors.New("cursor_invalid")
	}
	return c, err
}
func (r *Router) entityRefAudience(req *http.Request, chat string) (*chataudience.Receipt, error) {
	user := UserFromContext(req.Context())
	if user == nil || !canRole(RoleFromContext(req.Context()), "read") {
		return nil, errors.New("denied")
	}
	receipt, err := chataudience.CaptureTrusted(req.Context(), r.db, chat, user.ID, nil)
	if err != nil {
		return nil, err
	}
	if receipt == nil || receipt.WorkspaceID != WorkspaceIDFromContext(req.Context()) {
		return nil, errors.New("denied")
	}
	return receipt, nil
}

// Only the current trusted-workspace boundary has a tombstone evaluator.
// Validate shape in SQL before pagination so hidden rows never affect page
// counts. Exactly three keys excludes unknown fields and duplicate-key tricks.
const entityRefVisibilitySQL = `((m.id IS NOT NULL AND op.verb<>'deleted') OR
 (m.id IS NULL AND op.verb='deleted' AND op.actor_type='human' AND op.provenance='human' AND op.run_id=''
 AND json_type(op.access_snapshot)='object'
 AND json_type(op.access_snapshot,'$.version')='integer' AND json_extract(op.access_snapshot,'$.version')=1
 AND json_extract(op.access_snapshot,'$.workspace_id')=op.workspace_id
 AND json_extract(op.access_snapshot,'$.visibility')='trusted_workspace'
 AND (SELECT COUNT(*) FROM json_each(op.access_snapshot))=3))`

func (r *Router) chatEntityRefs(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	chat := req.PathValue("chatId")
	audience, err := r.entityRefAudience(req, chat)
	if err != nil {
		replyError(w, 404, "Chat not found")
		return
	}
	limit := 100
	if raw := req.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			replyError(w, 400, "limit_invalid")
			return
		}
	}
	c := entityCursor{}
	reset := true
	if raw := req.URL.Query().Get("cursor"); raw != "" {
		c, err = r.decodeEntityCursor(raw)
		if err != nil || c.Audience.UserID != audience.UserID || c.Audience.ChatID != chat || c.Audience.WorkspaceID != audience.WorkspaceID {
			replyError(w, 400, "cursor_invalid")
			return
		}
		reset = c.Complete || c.Audience != *audience
	}
	tx, err := r.db.BeginTx(req.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyError(w, 503, "entity_refs_unavailable")
		return
	}
	defer tx.Rollback()
	if reset {
		c = entityCursor{Audience: *audience, Expires: time.Now().Add(15 * time.Minute).Unix()}
		token := make([]byte, 18)
		if _, err = rand.Read(token); err != nil {
			replyError(w, 503, "entity_refs_unavailable")
			return
		}
		c.Snapshot = base64.RawURLEncoding.EncodeToString(token)
		// Visibility is the existing trusted issue GET predicate: exact workspace.
		// Only exact versioned deletion snapshots are accepted for missing issues.
		// Never page/count unauthorized raw refs.
		err = tx.QueryRowContext(req.Context(), `SELECT COALESCE(MAX(cr.chat_seq),0) FROM chat_entity_refs cr JOIN agent_operations op ON op.id=cr.operation_id LEFT JOIN missions m ON m.id=op.entity_id AND m.workspace_id=op.workspace_id WHERE cr.chat_id=? AND op.workspace_id=? AND op.kind='issue' AND op.status='completed' AND `+entityRefVisibilitySQL, chat, audience.WorkspaceID).Scan(&c.Upper)
		if err != nil {
			replyError(w, 503, "entity_refs_unavailable")
			return
		}
	}
	rows, err := tx.QueryContext(req.Context(), `SELECT cr.event_id,cr.chat_seq,cr.operation_id,cr.run_id,cr.trigger_message_id,cr.created_at,COALESCE(m.identifier,''),m.id IS NULL FROM chat_entity_refs cr JOIN agent_operations op ON op.id=cr.operation_id LEFT JOIN missions m ON m.id=op.entity_id AND m.workspace_id=op.workspace_id WHERE cr.chat_id=? AND op.workspace_id=? AND op.kind='issue' AND op.status='completed' AND `+entityRefVisibilitySQL+` AND cr.chat_seq>? AND cr.chat_seq<=? ORDER BY cr.chat_seq LIMIT ?`, chat, audience.WorkspaceID, c.After, c.Upper, limit+1)
	if err != nil {
		replyError(w, 503, "entity_refs_unavailable")
		return
	}
	type entry struct {
		ref        chatentities.Ref
		identifier string
		deleted    bool
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		err = rows.Scan(&e.ref.EventID, &e.ref.Seq, &e.ref.OperationID, &e.ref.RunID, &e.ref.TriggerMessageID, &e.ref.CreatedAt, &e.identifier, &e.deleted)
		if err != nil {
			break
		}
		entries = append(entries, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		replyError(w, 503, "entity_refs_unavailable")
		return
	}
	page := EntityRefPage{Reset: reset, SnapshotID: c.Snapshot, Refs: []EntityRefResponse{}, HasMore: len(entries) > limit}
	if page.HasMore {
		entries = entries[:limit]
	}
	for _, e := range entries {
		op, e2 := chatentities.GetOperation(req.Context(), tx, audience.WorkspaceID, e.ref.OperationID)
		if e2 != nil {
			replyError(w, 503, "entity_refs_unavailable")
			return
		}
		e.ref.Operation = *op
		card := EntityRefResponse{EventID: e.ref.EventID, Kind: op.Kind, Verb: op.Verb, Entity: EntityRefEntity{ID: op.EntityID, Identifier: e.identifier, WorkspaceID: op.WorkspaceID, Deleted: e.deleted}, Provenance: EntityRefProvenance{Actor: EntityRefActor{Type: op.ActorType, ID: op.ActorID}, At: op.CreatedAt}, Anchor: EntityRefAnchor{TriggerMessageID: e.ref.TriggerMessageID, RunID: e.ref.RunID}, RevisionAtAction: EntityRefRevision{Revision: op.Revision, AggregateRevision: op.AggregateRevision}}
		card.Anchor.RunStatus, err = entityRunStatus(req.Context(), tx, op.WorkspaceID, e.ref.RunID)
		if err != nil {
			replyError(w, 503, "entity_refs_unavailable")
			return
		}
		page.Refs = append(page.Refs, card)
		c.After = e.ref.Seq
	}
	c.Complete = !page.HasMore
	page.SnapshotComplete = c.Complete
	page.NextCursor, err = r.encodeEntityCursor(c)
	if err != nil {
		replyError(w, 503, "entity_refs_unavailable")
		return
	}
	// Recheck after gathering results: a changed audience cannot receive a stale
	// page. Retrying without a cursor obtains a new authorized snapshot.
	if err = tx.Commit(); err != nil {
		replyError(w, 503, "entity_refs_unavailable")
		return
	}
	current, err := r.entityRefAudience(req, chat)
	if err != nil || *current != *audience {
		replyError(w, 404, "Chat not found")
		return
	}
	writeJSON(w, 200, page)
}
func entityRunStatus(ctx context.Context, q chatentities.Queryer, workspace, run string) (*string, error) {
	if run == "" {
		return nil, nil
	}
	var kind string
	err := q.QueryRowContext(ctx, `SELECT entry_type FROM journal_entries WHERE workspace_id=? AND trace_id=? AND entry_type IN ('run.started','run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop') ORDER BY CASE WHEN entry_type='run.started' THEN 1 ELSE 0 END,ts DESC,id DESC LIMIT 1`, workspace, run).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status := map[string]string{"run.started": "running", "run.completed": "completed", "run.failed": "failed", "run.cancelled": "cancelled", "run.timeout": "timeout", "run.recovered_stop": "cancelled"}[kind]
	return &status, nil
}

// Operation read never serializes keys, hashes, receipts, access snapshots or
// arbitrary stored result JSON. The same viewer-authorized reference projection
// is its result. An operation without a readable origin chat is unavailable.
func (r *Router) chatEntityOperation(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	if user == nil || !canRole(RoleFromContext(req.Context()), "read") {
		replyError(w, 404, "Operation not found")
		return
	}
	workspace := WorkspaceIDFromContext(req.Context())
	op, err := chatentities.GetOperation(req.Context(), r.db, workspace, req.PathValue("operationId"))
	if err != nil || op.Status != "completed" {
		replyError(w, 404, "Operation not found")
		return
	}
	var identifier string
	var deleted bool
	err = r.db.QueryRowContext(req.Context(), `SELECT COALESCE(m.identifier,''),m.id IS NULL FROM agent_operations op LEFT JOIN missions m ON m.id=op.entity_id AND m.workspace_id=op.workspace_id WHERE op.id=? AND op.workspace_id=? AND op.kind='issue' AND `+entityRefVisibilitySQL, op.ID, workspace).Scan(&identifier, &deleted)
	if err != nil {
		replyError(w, 404, "Operation not found")
		return
	}
	rows, err := r.db.QueryContext(req.Context(), `SELECT DISTINCT chat_id FROM chat_entity_refs WHERE operation_id=? LIMIT 101`, op.ID)
	if err != nil {
		replyError(w, 503, "operation_unavailable")
		return
	}
	var chats []string
	for rows.Next() {
		var chat string
		if err = rows.Scan(&chat); err != nil {
			break
		}
		chats = append(chats, chat)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		replyError(w, 503, "operation_unavailable")
		return
	}
	for _, chat := range chats {
		if _, e := r.entityRefAudience(req, chat); e == nil {
			writeJSON(w, 200, map[string]any{"id": op.ID, "status": op.Status, "kind": op.Kind, "verb": op.Verb, "entity": EntityRefEntity{ID: op.EntityID, Identifier: identifier, WorkspaceID: workspace, Deleted: deleted}, "provenance": EntityRefProvenance{Actor: EntityRefActor{Type: op.ActorType, ID: op.ActorID}, At: op.CreatedAt}, "revision_at_action": EntityRefRevision{Revision: op.Revision, AggregateRevision: op.AggregateRevision}})
			return
		}
	}
	replyError(w, 404, "Operation not found")
}

// issueEntityRevision uses the same workspace-scoped read predicate as issue
// GET. Restricted sessions are excluded both by middleware and here.
func (r *Router) issueEntityRevision(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	if user == nil || !canRole(RoleFromContext(req.Context()), "read") {
		replyError(w, 404, "Issue not found")
		return
	}
	var restricted bool
	err := r.db.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')`, user.ID).Scan(&restricted)
	if err != nil || restricted {
		replyError(w, 404, "Issue not found")
		return
	}
	workspace := WorkspaceIDFromContext(req.Context())
	if pathWorkspace := req.PathValue("workspaceId"); pathWorkspace != "" && pathWorkspace != workspace {
		replyError(w, 404, "Issue not found")
		return
	}
	var revision EntityRefRevision
	err = r.db.QueryRowContext(req.Context(), `SELECT m.revision,m.aggregate_revision FROM missions m JOIN workspaces ws ON ws.id=m.workspace_id AND ws.deleted_at IS NULL JOIN workspace_members wm ON wm.workspace_id=m.workspace_id AND wm.user_id=? AND wm.access_mode='trusted' WHERE (m.identifier=? OR m.id=?) AND m.workspace_id=? ORDER BY (m.identifier=?) DESC LIMIT 1`, user.ID, req.PathValue("identifier"), req.PathValue("identifier"), workspace, req.PathValue("identifier")).Scan(&revision.Revision, &revision.AggregateRevision)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, 404, "Issue not found")
		return
	}
	if err != nil {
		replyError(w, 503, "revision_unavailable")
		return
	}
	writeJSON(w, 200, revision)
}
