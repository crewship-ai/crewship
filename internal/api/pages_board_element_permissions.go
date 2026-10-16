package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type boardPermissionElement struct {
	ElementID   string `json:"element_id"`
	ExpectedRev int64  `json:"expected_rev"`
}
type boardElementPermission struct {
	ID        string                   `json:"id"`
	RunID     string                   `json:"run_id"`
	AgentID   string                   `json:"agent_id"`
	AgentName string                   `json:"agent_name"`
	Elements  []boardPermissionElement `json:"elements"`
	Methods   []string                 `json:"methods"`
	Purpose   string                   `json:"purpose"`
	ExpiresAt string                   `json:"expires_at"`
	CreatedAt string                   `json:"created_at"`
	Used      bool                     `json:"used"`
	Revoked   bool                     `json:"revoked"`
}

func validBoardPermissionScope(elements []boardPermissionElement, methods []string, purpose string, seconds int) bool {
	if len(elements) < 1 || len(elements) > 100 || len(methods) < 1 || len(methods) > 2 || seconds < 1 || seconds > 3600 || len(purpose) > 1024 || strings.TrimSpace(purpose) == "" || !utf8.ValidString(purpose) {
		return false
	}
	seen := map[string]bool{}
	for _, e := range elements {
		if !boardMutationValid(e.ElementID) || e.ExpectedRev < 1 || seen[e.ElementID] {
			return false
		}
		seen[e.ElementID] = true
	}
	seen = map[string]bool{}
	for _, m := range methods {
		if (m != "update" && m != "delete") || seen[m] {
			return false
		}
		seen[m] = true
	}
	return true
}
func (h *PageHandler) boardPermissionPrincipal(ctx context.Context, rec *pageRecord, run string) (*artifactRunAuthority, string, error) {
	a := &artifactRunAuthority{}
	var expiry string
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT workspace_id,agent_id,crew_id,run_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=?`, run, WorkspaceIDFromContext(ctx)).Scan(&a.WorkspaceID, &a.AgentID, &a.CrewID, &a.RunID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expiry)
	if err != nil {
		return nil, "", err
	}
	if err = h.checkBoardRun(ctx, rec, a); err != nil {
		return nil, "", err
	}
	return a, expiry, nil
}
func (h *PageHandler) humanBoardPermissionTransaction(w http.ResponseWriter, r *http.Request) (*sql.Tx, *http.Request, *pageRecord, bool) {
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil || artifactAgentID(r) != "" {
		replyError(w, 401, "human authentication required")
		return nil, r, nil, false
	}
	tx, r, rec, ok := h.boardTransaction(w, r)
	if !ok {
		return nil, r, nil, false
	}
	if !rec.HasBoard {
		tx.Rollback()
		replyError(w, 404, "board not found")
		return nil, r, nil, false
	}
	return tx, r, rec, true
}
func (h *PageHandler) GrantBoardElementPermission(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	var req struct {
		MutationID       string                   `json:"mutation_id"`
		RunID            string                   `json:"run_id"`
		Elements         []boardPermissionElement `json:"elements"`
		Methods          []string                 `json:"methods"`
		Purpose          string                   `json:"purpose"`
		ExpiresInSeconds int                      `json:"expires_in_seconds"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if _, ok := boardDecode(w, r, &req); !ok {
		return
	}
	if !boardMutationValid(req.MutationID) || !boardMutationValid(req.RunID) || !validBoardPermissionScope(req.Elements, req.Methods, req.Purpose, req.ExpiresInSeconds) {
		replyError(w, 422, "bounded elements, exact update/delete methods, purpose and 1..3600 expiry seconds required")
		return
	}
	tx, r, rec, ok := h.humanBoardPermissionTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	a, limit, err := h.boardPermissionPrincipal(r.Context(), rec, req.RunID)
	if err != nil {
		replyError(w, 403, "current delegated human chat run required")
		return
	}
	fingerprint := boardFingerprint("human-element-permission", req)
	var existingID, digest, expiryExisting, createdExisting string
	var usedExisting, revokedExisting bool
	err = tx.QueryRowContext(r.Context(), `SELECT id,request_digest,expires_at,created_at,used_mutation_id IS NOT NULL,revoked_at IS NOT NULL FROM page_board_element_permissions WHERE board_page_id=? AND granted_by_user_id=? AND mutation_id=?`, rec.ID, UserFromContext(r.Context()).ID, req.MutationID).Scan(&existingID, &digest, &expiryExisting, &createdExisting, &usedExisting, &revokedExisting)
	if err == nil {
		if digest != fingerprint {
			artifactCommentError(w, 409, "mutation_conflict", "mutation_id was used for another permission intent")
			return
		}
		v := boardElementPermission{ID: existingID, RunID: a.RunID, AgentID: a.AgentID, Elements: req.Elements, Methods: req.Methods, Purpose: req.Purpose, ExpiresAt: expiryExisting, CreatedAt: createdExisting, Used: usedExisting, Revoked: revokedExisting}
		_ = tx.QueryRowContext(r.Context(), `SELECT name FROM agents WHERE id=?`, a.AgentID).Scan(&v.AgentName)
		writeJSON(w, 200, v)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.boardError(w, err)
		return
	}
	var count int
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM page_board_element_permissions WHERE board_page_id=?`, rec.ID).Scan(&count); err != nil {
		h.boardError(w, err)
		return
	}
	if count >= 1000 {
		artifactCommentError(w, 409, "permission_limit", "board permission limit reached")
		return
	}
	for _, element := range req.Elements {
		var rev int64
		var agent sql.NullString
		var raw sql.NullString
		err = tx.QueryRowContext(r.Context(), `SELECT element_rev,author_agent_id,element_json FROM page_board_elements WHERE board_page_id=? AND element_id=?`, rec.ID, element.ElementID).Scan(&rev, &agent, &raw)
		if err != nil || !raw.Valid || agent.Valid || rev != element.ExpectedRev {
			artifactCommentError(w, 409, "stale_element", "permission requires an existing human-owned element at its current revision")
			return
		}
	}
	deadline, err := time.Parse(time.RFC3339Nano, limit) // tsformat:allow: comparing stored authority expiry; canonical write below
	if err != nil {
		replyError(w, 403, "run expiry unavailable")
		return
	}
	now := time.Now()
	expiry := now.Add(time.Duration(req.ExpiresInSeconds) * time.Second)
	if deadline.Before(expiry) {
		expiry = deadline
	}
	v := boardElementPermission{ID: generateCUID(), RunID: a.RunID, AgentID: a.AgentID, Elements: req.Elements, Methods: req.Methods, Purpose: req.Purpose, ExpiresAt: tsformat.Format(expiry), CreatedAt: tsformat.Format(now)}
	_ = tx.QueryRowContext(r.Context(), `SELECT name FROM agents WHERE id=?`, a.AgentID).Scan(&v.AgentName)
	elements, _ := json.Marshal(req.Elements)
	methods, _ := json.Marshal(req.Methods)
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO page_board_element_permissions(id,board_page_id,run_id,agent_id,granted_by_user_id,mutation_id,request_digest,elements_json,methods_json,purpose,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, rec.ID, a.RunID, a.AgentID, UserFromContext(r.Context()).ID, req.MutationID, fingerprint, string(elements), string(methods), req.Purpose, v.ExpiresAt, v.CreatedAt); err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	writeJSON(w, 201, v)
}
func (h *PageHandler) RevokeBoardElementPermission(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	tx, r, rec, ok := h.humanBoardPermissionTransaction(w, r)
	if !ok {
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `UPDATE page_board_element_permissions SET revoked_at=COALESCE(revoked_at,?),revoked_by_user_id=COALESCE(revoked_by_user_id,?) WHERE id=? AND board_page_id=?`, tsformat.Format(time.Now()), UserFromContext(r.Context()).ID, r.PathValue("permissionId"), rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if n, _ := result.RowsAffected(); n != 1 {
		replyError(w, 404, "permission not found")
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	w.WriteHeader(204)
}

// The caller already holds the Board writer fence and rechecked the live run.
// Return original authors for explicitly delegated human nodes; graph closure
// still treats every outside protected node as protected.
func (h *PageHandler) useBoardElementPermission(ctx context.Context, tx *sql.Tx, rec *pageRecord, permission, mutation string, current, next pages.BoardScene, batch pages.BoardBatch, authors map[string]boardElementAuthor, a *artifactRunAuthority) (map[string]boardElementAuthor, error) {
	effective := make(map[string]boardElementAuthor, len(authors))
	for id, author := range authors {
		effective[id] = author
	}
	preserved := map[string]boardElementAuthor{}
	if permission == "" {
		return preserved, protectBoardHumanElements(current, next, batch, effective, a.AgentID)
	}
	var run, agent, granter, rawElements, rawMethods, expiry string
	var used, revoked sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT run_id,agent_id,granted_by_user_id,elements_json,methods_json,expires_at,used_mutation_id,revoked_at FROM page_board_element_permissions WHERE id=? AND board_page_id=?`, permission, rec.ID).Scan(&run, &agent, &granter, &rawElements, &rawMethods, &expiry, &used, &revoked)
	denied := errors.New("human element permission unavailable, expired, used or outside scope")
	if err != nil || run != a.RunID || agent != a.AgentID || used.Valid || revoked.Valid {
		return nil, denied
	}
	deadline, err := time.Parse(time.RFC3339Nano, expiry) // tsformat:allow: comparing stored permission expiry; no write
	if err != nil || !time.Now().Before(deadline) {
		return nil, denied
	}
	var role string
	if tx.QueryRowContext(ctx, `SELECT wm.role FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted')`, a.WorkspaceID, granter).Scan(&role) != nil || !h.mayEditSpec(ctx, a.WorkspaceID, granter, role, rec) {
		return nil, denied
	}
	var elements []boardPermissionElement
	var methods []string
	if json.Unmarshal([]byte(rawElements), &elements) != nil || json.Unmarshal([]byte(rawMethods), &methods) != nil {
		return nil, denied
	}
	allowed := map[string]int64{}
	for _, e := range elements {
		allowed[e.ElementID] = e.ExpectedRev
	}
	method := map[string]bool{}
	for _, m := range methods {
		method[m] = true
	}
	changed := map[string]string{}
	for _, up := range batch.Upserts {
		el, e := pages.ValidateBoardElement(up.Element)
		if e != nil {
			return nil, e
		}
		changed[el.ID] = "update"
	}
	for _, d := range batch.Deletes {
		changed[d.ID] = "delete"
	}
	for id, op := range changed {
		old, exists := current.Records[id]
		if !exists || authors[id].AgentID == a.AgentID {
			continue
		}
		if old.Deleted || authors[id].AgentID != "" || allowed[id] != old.ElementRev || !method[op] {
			return nil, denied
		}
		preserved[id] = authors[id]
		effective[id] = boardElementAuthor{AgentID: a.AgentID}
	}
	if len(preserved) == 0 {
		return nil, denied
	}
	if err = protectBoardHumanElements(current, next, batch, effective, a.AgentID); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE page_board_element_permissions SET used_mutation_id=? WHERE id=? AND used_mutation_id IS NULL AND revoked_at IS NULL`, mutation, permission)
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, denied
	}
	return preserved, nil
}

func (h *PageHandler) ListBoardElementPermissions(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if agent, _ := pageGrantCallerIsAgent(r); agent || projectAgentFrom(r.Context()) != nil || artifactAgentID(r) != "" {
		replyError(w, 401, "human authentication required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.boardPage(w, r, true)
	if !ok {
		return
	}
	if !rec.HasBoard {
		replyError(w, 404, "board not found")
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT p.id,p.run_id,p.agent_id,COALESCE(a.name,''),p.elements_json,p.methods_json,p.purpose,p.expires_at,p.created_at,p.used_mutation_id IS NOT NULL,p.revoked_at IS NOT NULL FROM page_board_element_permissions p LEFT JOIN agents a ON a.id=p.agent_id WHERE p.board_page_id=? ORDER BY p.created_at DESC,p.id DESC LIMIT 100`, rec.ID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	permissions := []boardElementPermission{}
	for rows.Next() {
		var v boardElementPermission
		var elements, methods string
		if err = rows.Scan(&v.ID, &v.RunID, &v.AgentID, &v.AgentName, &elements, &methods, &v.Purpose, &v.ExpiresAt, &v.CreatedAt, &v.Used, &v.Revoked); err != nil {
			rows.Close()
			h.boardError(w, err)
			return
		}
		if err = json.Unmarshal([]byte(elements), &v.Elements); err != nil {
			rows.Close()
			h.boardError(w, err)
			return
		}
		if err = json.Unmarshal([]byte(methods), &v.Methods); err != nil {
			rows.Close()
			h.boardError(w, err)
			return
		}
		permissions = append(permissions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		h.boardError(w, err)
		return
	}
	rows, err = tx.QueryContext(r.Context(), `SELECT p.run_id,COALESCE(a.name,''),d.run_id IS NULL FROM artifact_run_principals p JOIN agents a ON a.id=p.agent_id LEFT JOIN page_board_run_delegations d ON d.run_id=p.run_id AND d.board_page_id=? WHERE p.workspace_id=? AND p.human_user_id=? AND (d.run_id IS NOT NULL OR ?=?) AND d.revoked_at IS NULL ORDER BY p.expires_at DESC LIMIT 100`, rec.ID, WorkspaceIDFromContext(r.Context()), rec.OwnerUserID, UserFromContext(r.Context()).ID, rec.OwnerUserID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	type candidate struct {
		run, name string
		needs     bool
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.run, &c.name, &c.needs); err != nil {
			rows.Close()
			h.boardError(w, err)
			return
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		h.boardError(w, err)
		return
	}
	runs := []map[string]any{}
	for _, c := range candidates {
		var a *artifactRunAuthority
		var expiry string
		var e error
		if !c.needs {
			a, expiry, e = h.boardPermissionPrincipal(r.Context(), rec, c.run)
		} else {
			a = &artifactRunAuthority{}
			e = tx.QueryRowContext(r.Context(), `SELECT workspace_id,agent_id,crew_id,run_id,human_user_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=?`, c.run).Scan(&a.WorkspaceID, &a.AgentID, &a.CrewID, &a.RunID, &a.HumanUserID, &a.ChatID, &a.MessageID, &expiry)
			if e == nil {
				e = h.checkArtifactRunPrincipal(r.Context(), a)
			}
			if e == nil {
				e = h.checkDocumentRunPageScope(r.Context(), rec.ID, a)
			}
			if e == nil && !h.agentMayEditSpec(r.Context(), a.WorkspaceID, rec, a.AgentID) {
				e = errors.New("agent page write grant required")
			}
		}
		if e == nil {
			runs = append(runs, map[string]any{"run_id": a.RunID, "agent_id": a.AgentID, "agent_name": c.name, "expires_at": expiry, "needs_board_delegation": c.needs})
		}
	}
	writeJSON(w, 200, map[string]any{"permissions": permissions, "eligible_runs": runs})
}

type boardElementPermissionManifest struct {
	ID        string                   `json:"id"`
	Elements  []boardPermissionElement `json:"elements"`
	Methods   []string                 `json:"methods"`
	Purpose   string                   `json:"purpose"`
	ExpiresAt string                   `json:"expires_at"`
}

func (h *PageHandler) boardAgentElementPermissions(ctx context.Context, rec *pageRecord, a *artifactRunAuthority) ([]boardElementPermissionManifest, error) {
	if err := h.checkBoardRun(ctx, rec, a); err != nil {
		return nil, err
	}
	q := h.pageAuthReader(ctx)
	rows, err := q.QueryContext(ctx, `SELECT id,elements_json,methods_json,purpose,expires_at,granted_by_user_id FROM page_board_element_permissions WHERE board_page_id=? AND run_id=? AND agent_id=? AND used_mutation_id IS NULL AND revoked_at IS NULL AND expires_at>? ORDER BY created_at DESC,id DESC LIMIT 100`, rec.ID, a.RunID, a.AgentID, tsformat.Format(time.Now()))
	if err != nil {
		return nil, err
	}
	type entry struct {
		manifest boardElementPermissionManifest
		granter  string
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		var elements, methods string
		if err = rows.Scan(&e.manifest.ID, &elements, &methods, &e.manifest.Purpose, &e.manifest.ExpiresAt, &e.granter); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(elements), &e.manifest.Elements); err != nil {
			rows.Close()
			return nil, err
		}
		if err = json.Unmarshal([]byte(methods), &e.manifest.Methods); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []boardElementPermissionManifest{}
	for _, e := range entries {
		var role string
		if q.QueryRowContext(ctx, `SELECT wm.role FROM workspace_members wm JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=? AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=wm.user_id AND restricted.access_mode='restricted')`, a.WorkspaceID, e.granter).Scan(&role) == nil && h.mayEditSpec(ctx, a.WorkspaceID, e.granter, role, rec) {
			result = append(result, e.manifest)
		}
	}
	return result, nil
}
