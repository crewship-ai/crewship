package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

type artifactReportedPhase struct {
	Phase      string `json:"phase"`
	Message    string `json:"message"`
	ReportedAt string `json:"reported_at"`
}
type artifactRequestReply struct {
	Body      string `json:"body"`
	AgentID   string `json:"agent_id,omitempty"`
	RunID     string `json:"run_id"`
	HumanID   string `json:"on_behalf_of_user_id,omitempty"`
	CreatedAt string `json:"created_at"`
}
type artifactRequestOutcome struct {
	ResultKind         string                 `json:"result_kind"`
	AgentReportedPhase *artifactReportedPhase `json:"agent_reported_phase,omitempty"`
	Reply              *artifactRequestReply  `json:"reply,omitempty"`
}

func loadArtifactRequestOutcome(ctx context.Context, q pageAuthQueryer, id string) (artifactRequestOutcome, error) {
	var out artifactRequestOutcome
	if err := q.QueryRowContext(ctx, `SELECT result_kind FROM page_artifact_requests WHERE id=?`, id).Scan(&out.ResultKind); err != nil {
		return out, err
	}
	var phase artifactReportedPhase
	err := q.QueryRowContext(ctx, `SELECT phase,message,created_at FROM page_artifact_request_updates WHERE request_id=? AND operation='progress' ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&phase.Phase, &phase.Message, &phase.ReportedAt)
	if err == nil {
		out.AgentReportedPhase = &phase
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var reply artifactRequestReply
	err = q.QueryRowContext(ctx, `SELECT body,COALESCE(agent_id,''),run_id,COALESCE(on_behalf_of_user_id,''),created_at FROM page_artifact_request_updates WHERE request_id=? AND operation='reply'`, id).Scan(&reply.Body, &reply.AgentID, &reply.RunID, &reply.HumanID, &reply.CreatedAt)
	if err == nil {
		out.Reply = &reply
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	return out, nil
}

// Reports are agent claims, never execution observations, leases or authority.
func (h *PageHandler) UpdateArtifactRequestAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	r, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	var body struct {
		MutationID string `json:"mutation_id"`
		Operation  string `json:"operation"`
		Phase      string `json:"phase,omitempty"`
		Message    string `json:"message,omitempty"`
		Body       string `json:"body,omitempty"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	rawBody, decodeErr := io.ReadAll(r.Body)
	if decodeErr != nil || validateArtifactRequestUpdateJSON(rawBody) != nil || json.Unmarshal(rawBody, &body) != nil || len(body.MutationID) < 1 || len(body.MutationID) > 128 || strings.TrimSpace(body.MutationID) != body.MutationID {
		replyError(w, 400, "one typed update and mutation_id required")
		return
	}
	valid := body.Operation == "progress" && (body.Phase == "preparing" || body.Phase == "waiting_for_input" || body.Phase == "uploading" || body.Phase == "finalizing") && body.Body == "" && len(body.Message) <= 4096 && utf8.ValidString(body.Message)
	valid = valid || body.Operation == "reply" && body.Phase == "" && body.Message == "" && len(body.Body) <= 4096 && strings.TrimSpace(body.Body) != "" && utf8.ValidString(body.Body)
	if !valid {
		replyError(w, 400, "invalid progress or reply update")
		return
	}
	raw, _ := json.Marshal(body)
	fingerprint := attachmentDigest(raw)
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	id := r.PathValue("requestId")
	if _, err = tx.ExecContext(r.Context(), `UPDATE page_artifact_requests SET updated_at=updated_at WHERE id=?`, id); err != nil {
		h.boardError(w, err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	a := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	var page, status, kind, deadline, updated string
	if tx.QueryRowContext(ctx, `SELECT page_id,status,result_kind,deadline_at,updated_at FROM page_artifact_requests WHERE id=? AND run_id=? AND agent_id=? AND human_user_id=? AND chat_id=?`, id, a.RunID, a.AgentID, a.HumanUserID, a.ChatID).Scan(&page, &status, &kind, &deadline, &updated) != nil {
		replyError(w, 403, "exact request run required")
		return
	}
	if err = h.checkArtifactRun(ctx, page, a); err != nil || a.RequestID != id || !pageRequestWithinDeadline(deadline, updated, time.Now()) {
		replyError(w, 403, "request authority expired or revoked")
		return
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT fingerprint FROM page_artifact_request_updates WHERE request_id=? AND run_id=? AND mutation_id=?`, id, a.RunID, body.MutationID).Scan(&old)
	if err == nil {
		if old != fingerprint {
			replyError(w, 409, "mutation_id reused with different update")
			return
		}
		h.writeArtifactRequestUpdateReceipt(w, ctx, tx, id, a.RunID, body.MutationID)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		h.boardError(w, err)
		return
	}
	if status != "running" {
		replyError(w, 409, "request is not running")
		return
	}
	if body.Operation == "reply" && kind != "reply" {
		replyError(w, 409, "request requires a published revision")
		return
	}
	var count int
	var last sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),MAX(created_at) FROM page_artifact_request_updates WHERE request_id=?`, id).Scan(&count, &last); err != nil {
		h.boardError(w, err)
		return
	}
	if count >= 100 || (body.Operation == "progress" && count >= 99) {
		replyError(w, 429, "request update limit reached")
		return
	}
	now := time.Now()
	if body.Operation == "progress" && last.Valid {
		at, e := parsePageRequestTime(last.String)
		if e != nil || now.Sub(at) < 5*time.Second {
			w.Header().Set("Retry-After", "5")
			replyError(w, 429, "progress update rate limit")
			return
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO page_artifact_request_updates(id,request_id,agent_id,run_id,on_behalf_of_user_id,mutation_id,fingerprint,operation,phase,message,body,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, generateCUID(), id, a.AgentID, a.RunID, a.HumanUserID, body.MutationID, fingerprint, body.Operation, body.Phase, body.Message, body.Body, tsformat.Format(now))
	if err != nil {
		h.boardError(w, err)
		return
	}
	if body.Operation == "reply" {
		status = "succeeded"
		if _, err = tx.ExecContext(ctx, `UPDATE page_artifact_requests SET status='succeeded',updated_at=?,notice_pending=1 WHERE id=? AND status='running' AND result_kind='reply'`, tsformat.Format(now), id); err != nil {
			h.boardError(w, err)
			return
		}
		if err = h.pageRequestNoticeTx(ctx, tx, "artifact", id); err != nil {
			h.boardError(w, err)
			return
		}
	}
	response, err := artifactRequestUpdateReceipt(ctx, tx, id, a.RunID, body.MutationID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		h.boardError(w, err)
		return
	}
	writeJSON(w, 200, response)
}

type artifactRequestUpdateResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	artifactRequestOutcome
}

func artifactRequestUpdateReceipt(ctx context.Context, q pageAuthQueryer, id, run, mutation string) (artifactRequestUpdateResponse, error) {
	response := artifactRequestUpdateResponse{ID: id, Status: "running"}
	var operation string
	var p artifactReportedPhase
	var b artifactRequestReply
	err := q.QueryRowContext(ctx, `SELECT r.result_kind,u.operation,u.phase,u.message,u.created_at,u.body,COALESCE(u.agent_id,''),u.run_id,COALESCE(u.on_behalf_of_user_id,'') FROM page_artifact_request_updates u JOIN page_artifact_requests r ON r.id=u.request_id WHERE u.request_id=? AND u.run_id=? AND u.mutation_id=?`, id, run, mutation).Scan(&response.ResultKind, &operation, &p.Phase, &p.Message, &p.ReportedAt, &b.Body, &b.AgentID, &b.RunID, &b.HumanID)
	if operation == "progress" {
		response.AgentReportedPhase = &p
	} else if operation == "reply" {
		response.Status = "succeeded"
		b.CreatedAt = p.ReportedAt
		response.Reply = &b
	}
	return response, err
}
func (h *PageHandler) writeArtifactRequestUpdateReceipt(w http.ResponseWriter, ctx context.Context, q pageAuthQueryer, id, run, mutation string) {
	out, err := artifactRequestUpdateReceipt(ctx, q, id, run, mutation)
	if err != nil {
		h.boardError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// One bounded query for the list; never fetch the update history per card.
func loadArtifactRequestOutcomes(ctx context.Context, q pageAuthQueryer, page string) (map[string]artifactRequestOutcome, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id,r.result_kind,COALESCE(p.phase,''),COALESCE(p.message,''),COALESCE(p.created_at,''),COALESCE(b.body,''),COALESCE(b.agent_id,''),COALESCE(b.run_id,''),COALESCE(b.on_behalf_of_user_id,''),COALESCE(b.created_at,'') FROM (SELECT id,result_kind,created_at FROM page_artifact_requests WHERE page_id=? ORDER BY created_at DESC,id DESC LIMIT 100) r LEFT JOIN page_artifact_request_updates p ON p.id=(SELECT id FROM page_artifact_request_updates WHERE request_id=r.id AND operation='progress' ORDER BY created_at DESC,id DESC LIMIT 1) LEFT JOIN page_artifact_request_updates b ON b.request_id=r.id AND b.operation='reply'`, page)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]artifactRequestOutcome{}
	for rows.Next() {
		var id string
		var v artifactRequestOutcome
		var p artifactReportedPhase
		var b artifactRequestReply
		if err = rows.Scan(&id, &v.ResultKind, &p.Phase, &p.Message, &p.ReportedAt, &b.Body, &b.AgentID, &b.RunID, &b.HumanID, &b.CreatedAt); err != nil {
			return nil, err
		}
		if p.Phase != "" {
			v.AgentReportedPhase = &p
		}
		if b.RunID != "" {
			v.Reply = &b
		}
		out[id] = v
	}
	return out, rows.Err()
}

func validateArtifactRequestUpdateJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid UTF8")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("object required")
	}
	seen := map[string]bool{}
	allowed := map[string]bool{"mutation_id": true, "operation": true, "phase": true, "message": true, "body": true}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return fmt.Errorf("unknown or duplicate field")
		}
		seen[key] = true
		var v any
		if err = dec.Decode(&v); err != nil {
			return err
		}
		if _, ok = v.(string); !ok {
			return fmt.Errorf("string required")
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
