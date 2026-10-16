package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// The registered decision route emits the decision variant, whose payload is
// always present. A1's generic in-process projection remains unchanged.
type chatDecisionWire struct {
	EventID          string                    `json:"event_id"`
	OperationID      string                    `json:"operation_id"`
	Kind             string                    `json:"kind"`
	Verb             string                    `json:"verb"`
	Entity           EntityRefEntity           `json:"entity"`
	Provenance       EntityRefProvenance       `json:"provenance"`
	Anchor           EntityRefAnchor           `json:"anchor"`
	RevisionAtAction EntityRefRevision         `json:"revision_at_action"`
	Decision         chatentities.DecisionCard `json:"decision"`
}

type textDecisionSource struct{ ID, Workspace, Chat, Agent, Run, Reason, Created, Deadline, Status, Action, Resolution, Actor, Resolved string }

func readTextDecision(ctx context.Context, q issueOperationQueryer, id, workspace string) (textDecisionSource, error) {
	var s textDecisionSource
	err := q.QueryRowContext(ctx, `SELECT id,workspace_id,chat_id,from_agent_id,COALESCE(source_run_id,''),reason,created_at,COALESCE(answer_deadline_at,''),status,COALESCE(action,''),COALESCE(resolution,''),COALESCE(resolved_by_user_id,''),COALESCE(resolved_at,'') FROM escalations WHERE id=? AND workspace_id=? AND type='TEXT' AND deleted_at IS NULL`, id, workspace).Scan(&s.ID, &s.Workspace, &s.Chat, &s.Agent, &s.Run, &s.Reason, &s.Created, &s.Deadline, &s.Status, &s.Action, &s.Resolution, &s.Actor, &s.Resolved)
	return s, err
}
func (s textDecisionSource) version() string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (r *Router) escalationDecision(ctx context.Context, a chataudience.Receipt, id string) (ChatCardProjection, error) {
	s, err := readTextDecision(ctx, r.db, id, a.WorkspaceID)
	if err != nil {
		return ChatCardProjection{}, err
	}
	if s.Chat != a.ChatID || s.Agent != a.AgentID || s.Run == "" {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	// Only persisted host run provenance may supply the triggering turn. Terminal
	// runs remain readable; no active-run authority admission is used here.
	var count int
	var raw string
	err = r.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(payload),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND agent_id=? AND entry_type='run.started'`, a.WorkspaceID, s.Run, s.Agent).Scan(&count, &raw)
	if err != nil {
		return ChatCardProjection{}, err
	}
	var origin struct {
		Chat    string                `json:"chat_id"`
		Trigger string                `json:"trigger_message_id"`
		Human   *chataudience.Receipt `json:"human_authority"`
	}
	if count != 1 || json.Unmarshal([]byte(raw), &origin) != nil || origin.Chat != s.Chat || origin.Trigger == "" || origin.Human == nil || origin.Human.ChatGeneration == "" || origin.Human.ChatGeneration != a.ChatGeneration || origin.Human.ChatID != a.ChatID || origin.Human.WorkspaceID != a.WorkspaceID || origin.Human.AgentID != a.AgentID {
		return ChatCardProjection{}, sql.ErrNoRows
	}
	var role string
	err = r.db.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE id=? AND access_mode='trusted'`, a.MemberID).Scan(&role)
	if err != nil {
		return ChatCardProjection{}, err
	}
	d := chatentities.DecisionCard{SchemaVersion: 1, Source: chatentities.DecisionSource{Type: "escalation", ID: s.ID, Version: s.version()}, DecisionType: "information", Status: "pending", Title: truncate(inbox.RedactSecrets(s.Reason), 240), Fields: []chatentities.DecisionField{{ID: "answer", Type: "textarea", Label: "Answer", Required: true}}, AllowedActions: []string{"view"}}
	if s.Deadline != "" {
		d.ExpiresAt = &s.Deadline
	}
	switch s.Status {
	case "PENDING":
		if canRole(role, "create") && canScope(ctx, "workspace:admin") {
			d.AllowedActions = append(d.AllowedActions, "answer")
		}
	case "EXPIRED":
		d.Status = "expired"
	case "CANCELLED":
		d.Status = "cancelled"
	case "RESOLVED":
		if s.Action == "redirect" {
			return ChatCardProjection{}, sql.ErrNoRows
		}
		var human int
		if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id=?`, s.Actor).Scan(&human); err != nil {
			return ChatCardProjection{}, err
		}
		if human != 1 {
			return ChatCardProjection{}, sql.ErrNoRows
		}
		d.Status = "answered"
		if s.Action == "reject" {
			d.Status = "rejected"
		}
		d.Resolution = &chatentities.DecisionResolution{AnsweredBy: s.Actor, AnsweredAt: s.Resolved, Values: map[string]json.RawMessage{"answer": decisionString(inbox.RedactSecrets(s.Resolution))}}
	default:
		return ChatCardProjection{}, sql.ErrNoRows
	}
	ref := ChatCardResponse{EntityRefResponse: EntityRefResponse{EventID: "decision:" + s.ID, Kind: "decision", Verb: "asked", Entity: EntityRefEntity{ID: s.ID, WorkspaceID: s.Workspace}, Provenance: EntityRefProvenance{Actor: EntityRefActor{Type: "agent", ID: s.Agent}, At: s.Created}, Anchor: EntityRefAnchor{RunID: s.Run, TriggerMessageID: origin.Trigger}}, Decision: &d}
	if err = validateE2Card(ref); err != nil {
		// Historical values outside the closed card contract stay on their
		// existing source surface; omit the entire unsupported source.
		return ChatCardProjection{}, sql.ErrNoRows
	}
	return ChatCardProjection{ChatID: s.Chat, Ref: ref}, nil
}

type decisionOperationKey struct{}
type decisionOperation struct {
	Audience           chataudience.Receipt
	Version, Key, Hash string
}

var errDecisionKeyCollision = errors.New("decision operation key reused with different intent")
var errDecisionVersion = errors.New("decision_version_conflict")
var errTextDecisionDenied = errors.New("decision access denied")

// The canonical Inbox resolver and chat adapter share this transaction and CAS.
// No notification is delivered until the caller observes a committed winner.
func (h *QueryHandler) resolveTextDecision(req *http.Request, id, workspace, user, resolution, action, redirect string) (bool, error) {
	tx, err := h.db.Conn(req.Context())
	if err != nil {
		return false, err
	}
	defer tx.Close()
	if _, err = tx.ExecContext(req.Context(), "BEGIN IMMEDIATE"); err != nil {
		return false, err
	}
	defer func() { _, _ = tx.ExecContext(context.WithoutCancel(req.Context()), "ROLLBACK") }()
	var role string
	err = tx.QueryRowContext(req.Context(), `SELECT wm.role FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=?`, workspace, user).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !canRole(role, "read") {
		return false, errDecisionDenied
	}
	if err != nil {
		return false, err
	}
	readable, readErr := chataudience.CanReadVerifiedEscalation(req.Context(), tx, workspace, id, user)
	if readErr != nil {
		return false, readErr
	}
	if !readable {
		return false, errDecisionDenied
	}
	op, _ := req.Context().Value(decisionOperationKey{}).(*decisionOperation)
	if op != nil {
		a, e := chataudience.CaptureTrustedQuery(req.Context(), tx, op.Audience.ChatID, user, &op.Audience)
		if e != nil {
			return false, e
		}
		if a == nil {
			return false, errDecisionDenied
		}
		source, sourceErr := readTextDecision(req.Context(), tx, id, workspace)
		if sourceErr != nil {
			return false, sourceErr
		}
		if source.Chat != a.ChatID || source.Agent != a.AgentID || source.Run == "" {
			return false, errDecisionDenied
		}
		var proofRaw string
		var proofCount int
		if err = tx.QueryRowContext(req.Context(), `SELECT COUNT(*),COALESCE(MAX(payload),'') FROM journal_entries WHERE workspace_id=? AND trace_id=? AND agent_id=? AND entry_type='run.started'`, workspace, source.Run, source.Agent).Scan(&proofCount, &proofRaw); err != nil {
			return false, err
		}
		var proof struct {
			Human *chataudience.Receipt `json:"human_authority"`
		}
		if proofCount != 1 || json.Unmarshal([]byte(proofRaw), &proof) != nil || proof.Human == nil || proof.Human.ChatGeneration == "" || proof.Human.ChatGeneration != a.ChatGeneration || proof.Human.ChatID != a.ChatID || proof.Human.WorkspaceID != a.WorkspaceID || proof.Human.AgentID != a.AgentID {
			return false, errDecisionDenied
		}
		var key, hash string
		if err = tx.QueryRowContext(req.Context(), `SELECT COALESCE(answer_operation_key,''),COALESCE(answer_operation_hash,'') FROM escalations WHERE id=? AND workspace_id=?`, id, workspace).Scan(&key, &hash); err != nil {
			return false, err
		}
		if key == op.Key {
			if hash != op.Hash {
				return false, errDecisionKeyCollision
			}
			return true, nil
		}
		if !canRole(role, "create") {
			return false, errDecisionDenied
		}
		if source.version() != op.Version {
			return false, errDecisionVersion
		}
	}
	if op == nil && !canRole(role, "create") {
		return false, errDecisionDenied
	}
	var key, hash any
	if op != nil {
		key = op.Key
		hash = op.Hash
	}
	now := tsformat.Format(time.Now())
	res, err := tx.ExecContext(req.Context(), `UPDATE escalations SET status='RESOLVED',resolution=?,action=?,redirect_to=NULLIF(?,''),resolved_at=?,resolved_by='user',resolved_by_user_id=?,answer_operation_key=?,answer_operation_hash=? WHERE id=? AND workspace_id=? AND type IN ('TEXT','LINK') AND deleted_at IS NULL AND status='PENDING' AND (answer_deadline_at IS NULL OR julianday(answer_deadline_at)>julianday(?))`, resolution, action, redirect, now, user, key, hash, id, workspace, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, errDecisionVersion
	}
	_, err = tx.ExecContext(req.Context(), "COMMIT")
	return false, err
}

func (r *Router) chatDecisionGet(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	adapter, ok := r.decisionSourceAdapterFor(req.PathValue("sourceType"))
	if !ok {
		replyError(w, 404, "Card not found")
		return
	}
	if r.queryHandler != nil {
		r.queryHandler.sweepExpiredEscalationsBestEffort(req.Context(), WorkspaceIDFromContext(req.Context()))
	}
	projected := httptest.NewRecorder()
	r.ChatCardProjectionHandler(adapter.Read)(projected, req)
	if projected.Code != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(projected.Code)
		_, _ = w.Write(projected.Body.Bytes())
		return
	}
	var card chatDecisionWire
	if err := json.Unmarshal(projected.Body.Bytes(), &card); err != nil {
		replyError(w, 500, "Could not read card")
		return
	}
	writeJSON(w, 200, card)
}
func (r *Router) chatDecisionList(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a, err := r.entityRefAudience(req, req.PathValue("chatId"))
	if err != nil || a == nil {
		replyError(w, 404, "Card not found")
		return
	}
	var eligible bool
	if err = r.db.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE id=? AND access_mode='trusted')`, a.MemberID).Scan(&eligible); err != nil {
		replyError(w, 503, "Could not read cards")
		return
	}
	if !eligible {
		replyError(w, 404, "Card not found")
		return
	}
	if r.queryHandler != nil {
		r.queryHandler.sweepExpiredEscalationsBestEffort(req.Context(), a.WorkspaceID)
	}

	limit := 100
	if raw := req.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			replyError(w, 400, "Invalid limit")
			return
		}
	}
	cursor := decisionPageCursor{Audience: *a, Snapshot: generateCUID(), Expires: time.Now().Add(15 * time.Minute).Unix()}
	reset := true
	if raw := req.URL.Query().Get("cursor"); raw != "" {
		cursor, err = r.decodeDecisionCursor(raw)
		if err != nil || cursor.Audience != *a {
			replyError(w, 400, "cursor_invalid")
			return
		}
		reset = false
	} else {
		err = r.db.QueryRowContext(req.Context(), `SELECT COALESCE(MAX(e.rowid),0) FROM escalations e WHERE `+textDecisionVisibilitySQL, a.WorkspaceID, a.ChatID, a.AgentID).Scan(&cursor.Upper)
		if err != nil {
			replyError(w, 503, "Could not read cards")
			return
		}
	}
	rows, err := r.db.QueryContext(req.Context(), `SELECT e.id,e.rowid FROM escalations e WHERE `+textDecisionVisibilitySQL+` AND e.rowid>? AND e.rowid<=? ORDER BY e.rowid LIMIT ?`, a.WorkspaceID, a.ChatID, a.AgentID, cursor.After, cursor.Upper, limit+1)
	if err != nil {
		replyError(w, 503, "Could not read cards")
		return
	}
	type selected struct {
		id       string
		position int64
	}
	ids := []selected{}
	for rows.Next() {
		var item selected
		if err = rows.Scan(&item.id, &item.position); err != nil {
			break
		}
		ids = append(ids, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		replyError(w, 503, "Could not read cards")
		return
	}
	hasMore := len(ids) > limit
	if hasMore {
		ids = ids[:limit]
	}
	refs := []ChatCardResponse{}
	for _, item := range ids {
		p, e := r.escalationDecision(req.Context(), *a, item.id)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			replyError(w, 503, "Could not read cards")
			return
		}
		refs = append(refs, p.Ref)
	}
	current, err := r.entityRefAudience(req, a.ChatID)
	if err != nil || current == nil || *current != *a {
		replyError(w, 404, "Card not found")
		return
	}
	next := ""
	if hasMore {
		cursor.After = ids[len(ids)-1].position
		next, err = r.encodeDecisionCursor(cursor)
		if err != nil {
			replyError(w, 503, "Could not read cards")
			return
		}
	}
	writeJSON(w, 200, ChatCardPage{ContractVersion: 1, Reset: reset, SnapshotID: cursor.Snapshot, SnapshotComplete: !hasMore, HasMore: hasMore, NextCursor: next, Refs: refs})

}
func (r *Router) chatDecisionAnswer(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if req.PathValue("sourceType") != "escalation" || r.queryHandler == nil {
		replyError(w, 404, "Card not found")
		return
	}
	a, err := r.entityRefAudience(req, req.PathValue("chatId"))
	if err != nil || a == nil {
		replyError(w, 404, "Card not found")
		return
	}
	r.queryHandler.sweepExpiredEscalationsBestEffort(req.Context(), a.WorkspaceID)
	if _, err = r.escalationDecision(req.Context(), *a, req.PathValue("sourceId")); err != nil {
		replyError(w, 404, "Card not found")
		return
	}
	var body chatentities.DecisionAnswer
	decoder := json.NewDecoder(io.LimitReader(req.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&body); err != nil {
		replyError(w, 400, "Invalid JSON")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		replyError(w, 400, "Invalid JSON")
		return
	}
	key := req.Header.Get("Idempotency-Key")
	if !cardText(key, 1, 200) || strings.TrimSpace(key) != key || body.SourceID != req.PathValue("sourceId") || body.Action != "answer" || body.ActionID != "" || len(body.Values) != 1 {
		replyError(w, 400, errDecisionInput.Error())
		return
	}
	var answer string
	answerErr := json.Unmarshal(body.Values["answer"], &answer)
	if answerErr != nil || !cardText(answer, 1, 10000) || !cardText(body.ExpectedVersion, 1, 128) {
		replyError(w, 400, "Answer is required")
		return
	}
	if inbox.RedactSecrets(answer) != answer {
		replyError(w, 400, "Credential values require the protected credential flow")
		return
	}
	intent, _ := json.Marshal(struct {
		User   string
		Answer chatentities.DecisionAnswer
	}{a.UserID, body})
	sum := sha256.Sum256(intent)
	op := &decisionOperation{Audience: *a, Version: body.ExpectedVersion, Key: key, Hash: fmt.Sprintf("%x", sum)}
	// Reuse the complete existing resolver: journal, Inbox mirroring and waiter
	// delivery run only for its committed CAS winner, never for replay.
	inner := req.Clone(context.WithValue(req.Context(), decisionOperationKey{}, op))
	inner.SetPathValue("escalationId", body.SourceID)
	data, _ := json.Marshal(map[string]string{"action": "approve", "resolution": answer})
	inner.Body = http.NoBody
	inner.Body = io.NopCloser(strings.NewReader(string(data)))
	recorder := httptest.NewRecorder()
	r.queryHandler.ResolveEscalation(recorder, inner)
	if recorder.Code == 409 {
		if strings.Contains(recorder.Body.String(), errDecisionKeyCollision.Error()) {
			replyError(w, 409, errDecisionKeyCollision.Error())
			return
		}
		if p, e := r.escalationDecision(req.Context(), *a, body.SourceID); e == nil {
			current, accessErr := r.entityRefAudience(req, a.ChatID)
			if accessErr != nil || current == nil || *current != *a {
				replyError(w, 404, "Card not found")
				return
			}
			writeJSON(w, 409, ChatCardVersionConflict{Error: "decision_version_conflict", Current: p.Ref})
			return
		}
	}
	if recorder.Code < 200 || recorder.Code >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
		return
	}
	r.chatDecisionGet(w, req)
}

func decisionString(value string) json.RawMessage { raw, _ := json.Marshal(value); return raw }

// Filter source visibility before pagination, so unsupported credentials,
// unanchored legacy rows and foreign origins never affect page boundaries.
const textDecisionVisibilitySQL = `e.workspace_id=? AND e.chat_id=? AND e.from_agent_id=? AND e.type='TEXT' AND e.deleted_at IS NULL AND COALESCE(e.source_run_id,'')<>''
 AND (e.status IN ('PENDING','EXPIRED','CANCELLED') OR (e.status='RESOLVED' AND e.action IN ('approve','reject') AND length(COALESCE(e.resolution,''))<=10000 AND EXISTS(SELECT 1 FROM users u WHERE u.id=e.resolved_by_user_id)))
 AND (SELECT COUNT(*) FROM journal_entries j WHERE j.workspace_id=e.workspace_id AND j.trace_id=e.source_run_id AND j.agent_id=e.from_agent_id AND j.entry_type='run.started')=1
 AND EXISTS(SELECT 1 FROM journal_entries j WHERE j.workspace_id=e.workspace_id AND j.trace_id=e.source_run_id AND j.agent_id=e.from_agent_id AND j.entry_type='run.started' AND json_valid(j.payload) AND json_extract(j.payload,'$.chat_id')=e.chat_id AND json_type(j.payload,'$.trigger_message_id')='text' AND length(json_extract(j.payload,'$.trigger_message_id')) BETWEEN 1 AND 200)`

type decisionPageCursor struct {
	Audience chataudience.Receipt `json:"a"`
	After    int64                `json:"p"`
	Upper    int64                `json:"u"`
	Snapshot string               `json:"s"`
	Expires  int64                `json:"e"`
}

func (r *Router) encodeDecisionCursor(cursor decisionPageCursor) (string, error) {
	cipher, err := r.entityCursorCipher()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, cipher.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(cipher.Seal(nonce, nonce, raw, []byte("chat-decisions:v1"))), nil
}
func (r *Router) decodeDecisionCursor(raw string) (decisionPageCursor, error) {
	var cursor decisionPageCursor
	if len(raw) > 4096 {
		return cursor, errors.New("cursor_invalid")
	}
	cipher, err := r.entityCursorCipher()
	if err != nil {
		return cursor, err
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) < cipher.NonceSize() {
		return cursor, errors.New("cursor_invalid")
	}
	data, err = cipher.Open(nil, data[:cipher.NonceSize()], data[cipher.NonceSize():], []byte("chat-decisions:v1"))
	if err != nil {
		return cursor, errors.New("cursor_invalid")
	}
	err = json.Unmarshal(data, &cursor)
	if err != nil || cursor.After < 0 || cursor.Upper < cursor.After || cursor.Snapshot == "" || cursor.Expires < time.Now().Unix() {
		return cursor, errors.New("cursor_invalid")
	}
	return cursor, nil
}
