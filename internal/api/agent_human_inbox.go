package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/groupchat"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

var errHumanInboxDisabled = errors.New("human inbox is disabled")

func requireHumanInboxWrite(w http.ResponseWriter, workspace string, enabled func(string) bool) bool {
	if enabled != nil && enabled(workspace) {
		return true
	}
	replyError(w, http.StatusNotFound, errHumanInboxDisabled.Error())
	return false
}

type humanInboxEntry struct {
	ID        string  `json:"id"`
	Kind      string  `json:"kind"`
	Content   string  `json:"content"`
	State     string  `json:"state"`
	Href      string  `json:"href"`
	CreatedAt string  `json:"created_at"`
	ReadAt    *string `json:"read_at"`
}

// Pure source projection. A human viewing this endpoint does not acknowledge
// delivery/read on behalf of an agent or acquire the source's execution rights.
func (r *Router) agentHumanInbox(w http.ResponseWriter, req *http.Request) {
	ws := WorkspaceIDFromContext(req.Context())
	user := UserFromContext(req.Context())
	agent := req.PathValue("agentId")
	if user == nil || ws == "" {
		replyError(w, 401, "authentication required")
		return
	}
	enabled, err := featureflags.IsEnabled(req.Context(), r.db, ws, featureflags.AgentHumanInbox)
	if err != nil {
		replyInternalError(w, r.logger, "check human inbox flag", err)
		return
	}
	if !enabled {
		replyError(w, http.StatusNotFound, "human inbox is disabled")
		return
	}
	var found int
	if err := r.db.QueryRowContext(req.Context(), `SELECT 1 FROM agents a JOIN workspaces w ON w.id=a.workspace_id WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND w.deleted_at IS NULL`, agent, ws).Scan(&found); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "agent not found")
		} else {
			replyInternalError(w, r.logger, "look up inbox agent", err)
		}
		return
	}
	out := []humanInboxEntry{}
	messages, err := groupchat.New(r.db).AgentInbox(req.Context(), ws, user.ID, agent)
	if err != nil {
		replyError(w, 503, "human inbox unavailable")
		return
	}
	for _, m := range messages {
		out = append(out, humanInboxEntry{ID: m.ID, Kind: m.Kind, Content: m.Content, State: m.State, CreatedAt: m.CreatedAt, ReadAt: m.ReadAt, Href: "/chat?conversation=" + url.QueryEscape(m.ConversationID) + "&workspace_id=" + url.QueryEscape(ws)})
	}
	// Issue titles already have a canonical Issue read surface. This projection
	// is deliberately limited to the caller's own human-created delegated work;
	// it does not publish another chat's derived assignment prompt or result.
	rows, err := r.db.QueryContext(req.Context(), `SELECT m.id,m.identifier,m.title,m.status,m.created_at,
 (SELECT receipt.read_at FROM issue_work iw
  CROSS JOIN assignments execution ON execution.id IN (
   SELECT direct.id FROM assignments direct WHERE direct.mission_id=m.id
   UNION SELECT mt.assignment_id FROM mission_tasks mt WHERE mt.mission_id=m.id AND mt.assignment_id IS NOT NULL)
  CROSS JOIN work_items source_work ON source_work.workspace_id=m.workspace_id AND source_work.domain_id=execution.id
   AND source_work.source='assignment' AND source_work.domain_kind='assignment' AND source_work.replay_of IS NULL AND source_work.agent_id=m.delegate_agent_id
  CROSS JOIN agent_inbox_receipts receipt ON receipt.workspace_id=m.workspace_id AND receipt.agent_id=m.delegate_agent_id
   AND receipt.source_kind='assignment' AND receipt.source_id=execution.id
  WHERE iw.mission_id=m.id AND receipt.source_revision=CAST(iw.brief_revision AS TEXT)
   AND execution.issue_brief_revision=iw.brief_revision AND receipt.source_sha256=source_work.input_sha256
  ORDER BY receipt.read_at LIMIT 1)
 FROM missions m JOIN agents a ON a.id=m.delegate_agent_id AND a.workspace_id=m.workspace_id
 WHERE m.workspace_id=? AND m.delegate_agent_id=? AND m.created_by_user_id=? AND a.deleted_at IS NULL
 AND EXISTS(SELECT 1 FROM workspace_members viewer JOIN users usr ON usr.id=viewer.user_id AND usr.suspended_at IS NULL JOIN workspaces live ON live.id=viewer.workspace_id AND live.deleted_at IS NULL WHERE viewer.workspace_id=m.workspace_id AND viewer.user_id=?
 AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=viewer.user_id AND restricted.access_mode='restricted'))
 ORDER BY m.created_at DESC,m.id DESC LIMIT 50`, ws, agent, user.ID, user.ID)
	if err != nil {
		replyError(w, 503, "human inbox unavailable")
		return
	}
	for rows.Next() {
		var e humanInboxEntry
		var ident string
		var read sql.NullString
		e.Kind = "issue"
		if err := rows.Scan(&e.ID, &ident, &e.Content, &e.State, &e.CreatedAt, &read); err != nil {
			rows.Close()
			replyError(w, 503, "human inbox unavailable")
			return
		}
		if read.Valid {
			e.ReadAt = &read.String
		}
		e.Href = "/issues/" + url.PathEscape(ident) + "?workspace_id=" + url.QueryEscape(ws)
		out = append(out, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyError(w, 503, "human inbox unavailable")
		return
	}
	// Host chat work already names the exact original trigger message. Reuse the
	// common current chat audience SQL before reading canonical text. Sealed
	// artifact conversations stay on their audited original surface.
	args := []any{ws, agent}
	args = append(args, chataudience.Args(user.ID)...)
	args = append(args, user.ID)
	rows, err = r.db.QueryContext(req.Context(), `SELECT m.id,m.content,w.state,m.ts,c.id,COALESCE(a.slug,''),receipt.read_at,receipt.source_sha256
 FROM work_items w INDEXED BY idx_agent_inbox_chat_work CROSS JOIN conversation_messages m ON m.id=w.source_ref AND m.role='user'
 JOIN chats c ON c.id=w.session_id AND c.id=m.session_id
 JOIN agents a ON a.id=w.agent_id AND a.id=m.agent_id AND a.workspace_id=w.workspace_id
 LEFT JOIN agent_inbox_receipts receipt ON receipt.workspace_id=w.workspace_id AND receipt.agent_id=a.id AND receipt.source_kind='chat_message' AND receipt.source_id=m.id
 WHERE w.workspace_id=? AND w.agent_id=? AND w.source='chat' AND w.domain_kind='host_chat_turn' AND a.deleted_at IS NULL
 AND c.workspace_id=w.workspace_id AND (`+chataudience.VisibleSQL+`) AND (`+chataudience.NoPrivateArtifactContextSQL+`)
 AND EXISTS(SELECT 1 FROM workspaces live WHERE live.id=c.workspace_id AND live.deleted_at IS NULL)
 AND EXISTS(SELECT 1 FROM users viewer WHERE viewer.id=? AND viewer.suspended_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=viewer.id AND restricted.access_mode='restricted'))
 ORDER BY w.created_at DESC,w.id DESC LIMIT 50`, args...)
	if err != nil {
		replyError(w, 503, "human inbox unavailable")
		return
	}
	for rows.Next() {
		var e humanInboxEntry
		var chat, slug string
		var read, digest sql.NullString
		e.Kind = "chat_message"
		if err := rows.Scan(&e.ID, &e.Content, &e.State, &e.CreatedAt, &chat, &slug, &read, &digest); err != nil {
			rows.Close()
			replyError(w, 503, "human inbox unavailable")
			return
		}
		if read.Valid && digest.String == groupchat.InboxSourceDigest(e.Content) {
			e.ReadAt = &read.String
		}
		e.Href = "/chat/" + url.PathEscape(slug) + "?session=" + url.QueryEscape(chat) + "&workspace_id=" + url.QueryEscape(ws)
		out = append(out, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		replyError(w, 503, "human inbox unavailable")
		return
	}
	// A grant can change while an HTTP request is reading the projections.
	// Recheck before returning anything read by the earlier statements.
	var canRead bool
	if err := r.db.QueryRowContext(req.Context(), `SELECT EXISTS (
		SELECT 1 FROM workspace_members member JOIN users viewer ON viewer.id=member.user_id
		JOIN workspaces workspace ON workspace.id=member.workspace_id
		WHERE member.workspace_id=? AND member.user_id=? AND viewer.suspended_at IS NULL
		 AND workspace.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM workspace_members restricted
		  WHERE restricted.user_id=viewer.id AND restricted.access_mode='restricted'))`,
		ws, user.ID).Scan(&canRead); err != nil {
		replyInternalError(w, r.logger, "recheck human inbox viewer", err)
		return
	}
	if !canRead {
		replyError(w, http.StatusForbidden, "human inbox is unavailable in this session")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, map[string]any{"items": out, "read_semantics": "Agent read is recorded only by an authenticated source-bound inbox_read tool call. Viewing this list does not mark agent messages read."})
}

// AgentInboxRead accepts no source/target selector. A cryptographically verified
// current run can read/ack only its host-recorded source, never another private
// human session served by the same agent. The receipt grants no mutation rights.
func (h *InternalHandler) AgentInboxRead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		RunID       string `json:"run_id"`
	}
	var rawRequest map[string]json.RawMessage
	if err := readJSONBounded(r, &rawRequest, 4096); err != nil {
		replyError(w, 400, "invalid inbox read")
		return
	}
	for field := range rawRequest {
		if field != "workspace_id" && field != "agent_id" && field != "run_id" {
			replyError(w, 400, "source selectors are not accepted")
			return
		}
	}
	rawBody, _ := json.Marshal(rawRequest)
	if json.Unmarshal(rawBody, &body) != nil || body.WorkspaceID == "" || body.AgentID == "" || body.RunID == "" {
		replyError(w, 400, "invalid inbox read")
		return
	}
	if !assertInternalTokenWorkspace(w, r, body.WorkspaceID) {
		return
	}
	boundCrew := InternalTokenCrewFromContext(r.Context())
	var crew string
	if err := h.db.QueryRowContext(r.Context(), `SELECT a.crew_id FROM agents a
		JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
		WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND (?='' OR c.id=?)`,
		body.AgentID, body.WorkspaceID, boundCrew, boundCrew).Scan(&crew); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "no human inbox source is available for this run")
		} else {
			replyInternalError(w, h.logger, "look up inbox run agent", err)
		}
		return
	}
	tw, ta, tr, valid := internaltoken.ValidateAgentRunToken(internaltoken.DeriveAgentRunKey(h.internalToken, body.WorkspaceID, crew), r.Header.Get("X-Crewship-Run-Token"))
	if !valid || tw != body.WorkspaceID || ta != body.AgentID || tr != body.RunID {
		replyError(w, 403, "agent run proof required")
		return
	}
	var result humanInboxEntry
	var notifyUser, sourceHash, sourceRevision string
	err := groupchat.WithWriteTransaction(r.Context(), h.db, func(q *sql.Conn) error {
		enabled, err := featureflags.IsEnabledWith(r.Context(), q, body.WorkspaceID, featureflags.AgentHumanInbox)
		if err != nil {
			return err
		}
		if !enabled {
			return errHumanInboxDisabled
		}
		var raw string
		// Exactly one host start, a still-running native run, and no terminal event.
		err = q.QueryRowContext(r.Context(), `SELECT j.payload FROM journal_entries j
 JOIN work_attempts current_attempt ON current_attempt.run_id=j.trace_id AND current_attempt.ended_at IS NULL
 JOIN work_items current_work ON current_work.id=current_attempt.work_id AND current_work.workspace_id=j.workspace_id AND current_work.agent_id=j.agent_id AND current_work.generation=current_attempt.generation AND current_work.state IN ('starting','running')
   JOIN agents a ON a.id=j.agent_id AND a.workspace_id=j.workspace_id AND a.crew_id=? AND a.deleted_at IS NULL
 JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
   WHERE j.trace_id=? AND j.workspace_id=? AND j.agent_id=? AND j.entry_type='run.started' AND current_work.crew_id=a.crew_id AND (j.actor_type='orchestrator' OR json_extract(j.payload,'$.host_admitted')=1)
 AND 1=(SELECT COUNT(*) FROM journal_entries duplicate WHERE duplicate.trace_id=j.trace_id AND duplicate.workspace_id=j.workspace_id AND duplicate.entry_type='run.started')
   AND NOT EXISTS(SELECT 1 FROM journal_entries ended WHERE ended.trace_id=j.trace_id AND ended.workspace_id=j.workspace_id AND ended.entry_type IN('run.completed','run.failed','run.cancelled','run.timeout','run.recovered_stop'))`, crew, body.RunID, body.WorkspaceID, body.AgentID).Scan(&raw)
		if err != nil {
			return err
		}
		var start struct {
			AssignmentID     string `json:"assignment_id"`
			ChatID           string `json:"chat_id"`
			TriggerMessageID string `json:"trigger_message_id"`
		}
		if json.Unmarshal([]byte(raw), &start) != nil {
			return runauthority.ErrUnavailable
		}
		if start.AssignmentID != "" {
			job, content, e := groupchat.ReadInboxAssignment(r.Context(), q, body.WorkspaceID, crew, body.AgentID, start.AssignmentID, body.RunID)
			if e == nil {
				result = humanInboxEntry{ID: job, Kind: "channel_message", Content: content}
				sourceHash = groupchat.InboxSourceDigest(content)
				if err := q.QueryRowContext(r.Context(), `SELECT requested_by_user_id FROM workspace_conversation_agent_jobs WHERE id=?`, job).Scan(&notifyUser); err != nil {
					return err
				}
			} else {
				if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				// A channel job that lost authority must never fall through to a generic
				// task prompt. Only a real mission task with live execution origin qualifies.
				var isChannel bool
				if err := q.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_conversation_agent_jobs WHERE assignment_id=?)`, start.AssignmentID).Scan(&isChannel); err != nil {
					return err
				}
				if isChannel {
					return groupchat.ErrForbidden
				}
				a, err := runauthority.Resolve(r.Context(), q, body.RunID, body.AgentID, body.WorkspaceID)
				if err != nil {
					return err
				}
				if a.HumanAuthority == nil {
					return runauthority.ErrUnavailable
				}
				var content string
				var brief int64
				err = q.QueryRowContext(r.Context(), `SELECT execution.task,iw.brief_revision,source_work.input_sha256
 FROM assignments execution JOIN mission_tasks mt ON mt.assignment_id=execution.id JOIN missions m ON m.id=mt.mission_id AND m.workspace_id=execution.workspace_id
 JOIN issue_work iw ON iw.mission_id=m.id AND iw.brief_revision=execution.issue_brief_revision
 JOIN work_items source_work ON source_work.workspace_id=execution.workspace_id AND source_work.agent_id=execution.assigned_to_id AND source_work.source='assignment' AND source_work.domain_id=execution.id
 JOIN work_attempts source_attempt ON source_attempt.work_id=source_work.id AND source_attempt.generation=source_work.generation AND source_attempt.run_id=? AND source_attempt.ended_at IS NULL
 WHERE execution.id=? AND execution.workspace_id=? AND execution.assigned_to_id=? AND execution.status='RUNNING' AND json_valid(source_work.input_json) AND json_extract(source_work.input_json,'$.task')=execution.task`, body.RunID, start.AssignmentID, body.WorkspaceID, body.AgentID).Scan(&content, &brief, &sourceHash)
				sourceRevision = strconv.FormatInt(brief, 10)
				if err != nil {
					return err
				}
				result = humanInboxEntry{ID: start.AssignmentID, Kind: "assignment", Content: content}
				if a.HumanAuthority != nil {
					notifyUser = a.HumanAuthority.UserID
				}
			}
		} else {
			a, err := runauthority.Resolve(r.Context(), q, body.RunID, body.AgentID, body.WorkspaceID)
			if err != nil {
				return err
			}
			if a.ChatID != start.ChatID || a.TriggerMessageID == "" || a.TriggerMessageID != start.TriggerMessageID || a.HumanAuthority == nil {
				return groupchat.ErrForbidden
			}
			var content string
			err = q.QueryRowContext(r.Context(), `SELECT message.content FROM conversation_messages message JOIN chats c ON c.id=message.session_id JOIN work_items source_work ON source_work.source='chat' AND source_work.domain_kind='host_chat_turn' AND source_work.source_ref=message.id AND source_work.session_id=message.session_id AND source_work.agent_id=message.agent_id JOIN work_attempts source_attempt ON source_attempt.work_id=source_work.id AND source_attempt.generation=source_work.generation AND source_attempt.run_id=? AND source_attempt.ended_at IS NULL WHERE message.id=? AND message.session_id=? AND message.agent_id=? AND message.role='user' AND (`+chataudience.NoPrivateArtifactContextSQL+`)`, body.RunID, a.TriggerMessageID, a.ChatID, body.AgentID).Scan(&content)
			if err != nil {
				return err
			}
			var committed string
			if err := q.QueryRowContext(r.Context(), `SELECT source_sha256 FROM agent_inbox_run_sources WHERE run_id=? AND workspace_id=? AND agent_id=? AND source_kind='chat_message' AND source_id=?`, body.RunID, body.WorkspaceID, body.AgentID, a.TriggerMessageID).Scan(&committed); err != nil {
				return err
			}
			if committed == "" || committed != groupchat.InboxSourceDigest(content) {
				return groupchat.ErrForbidden
			}
			sourceHash = committed
			result = humanInboxEntry{ID: a.TriggerMessageID, Kind: "chat_message", Content: content}
			notifyUser = a.HumanAuthority.UserID
		}
		now := tsformat.Format(time.Now())
		if _, err := q.ExecContext(r.Context(), `INSERT INTO agent_inbox_receipts(workspace_id,agent_id,source_kind,source_id,run_id,read_at,source_sha256,source_revision) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,agent_id,source_kind,source_id) DO UPDATE SET run_id=excluded.run_id,read_at=excluded.read_at,source_sha256=excluded.source_sha256,source_revision=excluded.source_revision WHERE agent_inbox_receipts.source_sha256<>excluded.source_sha256 OR agent_inbox_receipts.source_revision<>excluded.source_revision`, body.WorkspaceID, body.AgentID, result.Kind, result.ID, body.RunID, now, sourceHash, sourceRevision); err != nil {
			return err
		}
		var read string
		if err := q.QueryRowContext(r.Context(), `SELECT read_at FROM agent_inbox_receipts WHERE workspace_id=? AND agent_id=? AND source_kind=? AND source_id=?`, body.WorkspaceID, body.AgentID, result.Kind, result.ID).Scan(&read); err != nil {
			return err
		}
		result.ReadAt = &read
		result.State = "read"
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errHumanInboxDisabled):
			replyError(w, http.StatusNotFound, "human inbox is disabled")
		case errors.Is(err, sql.ErrNoRows), errors.Is(err, groupchat.ErrForbidden),
			errors.Is(err, runauthority.ErrIdentityMismatch), errors.Is(err, runauthority.ErrUnavailable),
			errors.Is(err, runauthority.ErrRequesterStale):
			replyError(w, http.StatusNotFound, "no human inbox source is available for this run")
		default:
			replyInternalError(w, h.logger, "read human inbox source", err)
		}
		return
	}
	if h.hub != nil && notifyUser != "" {
		h.hub.BroadcastChannel("user", notifyUser, "inbox.updated", map[string]string{"source": "agent_read_receipt"})
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, result)
}
