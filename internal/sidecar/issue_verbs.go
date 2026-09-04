package sidecar

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/untrusted"
)

// issue_verbs.go — the read/update/comment/link half of the agent's issue
// surface. Before this file the sidecar exposed exactly one issue verb,
// POST /issue/create, so an agent could open an issue and then never touch the
// board again: it could not comment on the issue it was working, move it to
// IN_PROGRESS, find a related one, or split a large issue into children. All
// four already existed on crewshipd's internal API (or, for relations, needed
// only the internal twin of a public handler); none of them were reachable from
// inside a container.
//
// Every handler here follows the shape handleIssueCreate established:
//
//   - the ACTING agent comes from the per-agent bearer token (#812), never from
//     the request body. A body-supplied agent_id is parsed and ignored, exactly
//     as crew_id is on the create path — see issueSpoofableFields below.
//   - the workspace comes from the trusted IPC identity, never the request.
//   - the crew is not sent at all: crewshipd derives it from the sidecar's
//     crew-bound (crwv1) internal token, which the agent cannot influence.
//
// so the only thing an agent controls is WHICH of its own crew's issues it
// touches and what it writes there.

// issueIdentInjectionChars mirrors missionIDInjectionChars (#1045) for issue
// identifiers. An identifier is taken from the URL path and concatenated into
// the internal IPC URL; a percent-encoded `?` or `&` in it would smuggle query
// parameters past the trusted scope query we append. Real identifiers are
// "<PREFIX>-<n>", so none of these characters is ever legitimate.
const issueIdentInjectionChars = missionIDInjectionChars

// issueIdentFromPath extracts and validates the identifier from a path of the
// form /issue/<IDENT>[/suffix]. Returns ok=false after writing the 400.
func issueIdentFromPath(w http.ResponseWriter, path, suffix string) (string, bool) {
	ident := strings.TrimPrefix(path, "/issue/")
	if suffix != "" {
		ident = strings.TrimSuffix(ident, suffix)
	}
	if ident == "" || strings.ContainsAny(ident, issueIdentInjectionChars) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "issue identifier required"})
		return "", false
	}
	return ident, true
}

// issueScopeQuery is the trusted workspace scope appended to every internal
// issue call. It is sourced from the IPC identity, so a request that also
// carries a workspace_id cannot widen it — crewshipd 403s a workspace_id that
// disagrees with its token binding, and this never sends a disagreeing one.
func (s *Server) issueScopeQuery() string {
	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	return "?" + q.Encode()
}

// issueActor is the common prelude: IPC configured, and the acting agent
// resolved from the bearer token. Returns ok=false after writing the response.
func (s *Server) issueActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return "", false
	}
	// #812: a forged/stale token is a 403, and once the crew has tokens an
	// omitted header is a downgrade attempt rather than a legacy caller.
	// Reads are gated too: the board is crew data, and a sibling that drops
	// its header must not read it as the boot agent.
	agentID, ok := s.actingAgentID(r)
	if !ok {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
		return "", false
	}
	return agentID, true
}

// issueListParams are the query parameters an agent may set on GET /issues.
// It is an ALLOWLIST, not a passthrough: forwarding the raw query would let an
// agent send crew_id (crewshipd 403s a disagreeing one, but a matching one is
// pointless noise) or mission_type=orchestration, which would turn the issue
// search into a mission dump. Everything not named here is dropped.
var issueListParams = []string{"q", "status", "assignee_id", "limit", "offset"}

// handleIssuesList handles GET /issues — search/list the crew's board.
//
// No crew_id is sent. For the crew-bound token a production sidecar holds, the
// binding CONSTRAINS the listing server-side (#1186 / effectiveCrewFilter), so
// the agent sees its own crew's issues and cannot ask for a sibling's; a
// workspace-bound sidecar keeps workspace-wide reach. Either way the scope is
// the token's, not the request's.
func (s *Server) handleIssuesList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.issueActor(w, r); !ok {
		return
	}

	q := url.Values{}
	q.Set("workspace_id", s.ipc.WorkspaceID)
	for _, name := range issueListParams {
		if v := r.URL.Query().Get(name); v != "" {
			q.Set(name, v)
		}
	}

	s.proxyToAPIFiltered(w, r, http.MethodGet, "/api/v1/internal/issues?"+q.Encode(), fenceIssueText)
}

// handleIssueGet handles GET /issue/{identifier} — read one issue.
func (s *Server) handleIssueGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.issueActor(w, r); !ok {
		return
	}
	ident, ok := issueIdentFromPath(w, r.URL.Path, "")
	if !ok {
		return
	}
	s.proxyToAPIFiltered(w, r, http.MethodGet,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+s.issueScopeQuery(), fenceIssueText)
}

// handleIssueComment handles POST /issue/{identifier}/comment.
//
// The comment is recorded with author_type='agent' and author_id = the ACTING
// agent. That is the whole point of the endpoint: before it, the only way an
// agent's words reached an issue was a human relaying them, and the only
// agent-authored rows on the board were the issues themselves.
func (s *Server) handleIssueComment(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.issueActor(w, r)
	if !ok {
		return
	}
	ident, ok := issueIdentFromPath(w, r.URL.Path, "/comment")
	if !ok {
		return
	}

	var req struct {
		Body string `json:"body"`
		// SECURITY: parsed and ignored. A crew shares one container and one
		// sidecar, so a request-supplied author would let any member post as
		// any other — the exact impersonation the per-agent token exists to
		// stop. Kept in the struct so the field is documented as refused
		// rather than silently unknown.
		AgentID     string `json:"agent_id"`
		WorkspaceID string `json:"workspace_id"` // SECURITY: ignored — IPC identity wins.
	}
	if !decodeCappedJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "body is required"})
		return
	}

	payload, err := json.Marshal(map[string]interface{}{
		"workspace_id": s.ipc.WorkspaceID,
		"agent_id":     agentID,
		"body":         req.Body,
	})
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPost,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+"/comments", "issue comment", payload)
}

// handleIssueCommentsList handles GET /issue/{identifier}/comments — the
// sidecar comment-READ verb §11.1 asks for (PRD-ISSUES-AND-ROUTINES-2026,
// work package B5, #2345): "today the sidecar can write a comment but
// cannot read the thread ... which is why an agent that wants history has
// no option but to be handed all of it." Before this an agent that wanted
// the comment thread had exactly one option: the full board dump the §11.1
// context-pack assembly deliberately does NOT auto-inject (mission_activity
// has no `commented` writer today — see issue_context_pack.go's own header
// comment). This is the tool call that fills that gap.
func (s *Server) handleIssueCommentsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.issueActor(w, r); !ok {
		return
	}
	ident, ok := issueIdentFromPath(w, r.URL.Path, "/comments")
	if !ok {
		return
	}
	s.proxyToAPIFiltered(w, r, http.MethodGet,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+"/comments"+s.issueScopeQuery(), fenceCommentText)
}

// handleIssueUpdate handles PATCH /issue/{identifier}.
//
// Fields are forwarded only when present, so a PATCH that carries just a status
// does not blank the assignee. crewshipd owns every validation that matters
// (status transitions, assignee/label workspace scoping, the crew fence); this
// handler's job is to make sure the workspace and the acting agent are the
// trusted ones.
func (s *Server) handleIssueUpdate(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.issueActor(w, r)
	if !ok {
		return
	}
	ident, ok := issueIdentFromPath(w, r.URL.Path, "")
	if !ok {
		return
	}

	var req struct {
		Status       string    `json:"status"`
		Priority     string    `json:"priority"`
		AssigneeType *string   `json:"assignee_type"`
		AssigneeID   *string   `json:"assignee_id"`
		DueDate      *string   `json:"due_date"`
		Estimate     *int      `json:"estimate"`
		Labels       *[]string `json:"labels"`
		Comment      *string   `json:"comment"`
		// SECURITY: parsed and ignored — see handleIssueComment.
		AgentID     string `json:"agent_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if !decodeCappedJSON(w, r, &req) {
		return
	}

	body := map[string]interface{}{
		"workspace_id": s.ipc.WorkspaceID,
		"agent_id":     agentID,
	}
	if req.Status != "" {
		body["status"] = req.Status
	}
	if req.Priority != "" {
		body["priority"] = req.Priority
	}
	if req.AssigneeType != nil {
		body["assignee_type"] = *req.AssigneeType
	}
	if req.AssigneeID != nil {
		body["assignee_id"] = *req.AssigneeID
	}
	if req.DueDate != nil {
		body["due_date"] = *req.DueDate
	}
	if req.Estimate != nil {
		body["estimate"] = *req.Estimate
	}
	if req.Labels != nil {
		body["labels"] = *req.Labels
	}
	if req.Comment != nil {
		body["comment"] = *req.Comment
	}
	if len(body) == 2 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "no fields to update"})
		return
	}

	payload, err := json.Marshal(body)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPatch,
		"/api/v1/internal/issues/"+url.PathEscape(ident), "issue update", payload)
}

// handleIssueLink handles POST /issue/{identifier}/link.
//
// relation_type is one of blocks | blocked_by | relates_to | duplicate_of |
// sub_issue_of. The last one is the decomposition verb — it makes the issue
// named in the path a CHILD of the target — and it is why this endpoint exists
// at all: splitting a large issue into per-agent children is the agentic use
// case, and until now an agent could create the children but never attach them.
func (s *Server) handleIssueLink(w http.ResponseWriter, r *http.Request) {
	agentID, ok := s.issueActor(w, r)
	if !ok {
		return
	}
	ident, ok := issueIdentFromPath(w, r.URL.Path, "/link")
	if !ok {
		return
	}

	var req struct {
		TargetIdentifier string `json:"target_identifier"`
		RelationType     string `json:"relation_type"`
		// SECURITY: parsed and ignored — see handleIssueComment.
		AgentID     string `json:"agent_id"`
		WorkspaceID string `json:"workspace_id"`
	}
	if !decodeCappedJSON(w, r, &req) {
		return
	}
	if req.TargetIdentifier == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "target_identifier is required"})
		return
	}
	if req.RelationType == "" {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "relation_type is required"})
		return
	}

	payload, err := json.Marshal(map[string]interface{}{
		"workspace_id":      s.ipc.WorkspaceID,
		"agent_id":          agentID,
		"target_identifier": req.TargetIdentifier,
		"relation_type":     req.RelationType,
	})
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize request"})
		return
	}
	s.proxyIPCJSON(w, r, http.MethodPost,
		"/api/v1/internal/issues/"+url.PathEscape(ident)+"/relations", "issue link", payload)
}

// fencedIssueFields are the issue fields that carry free text a human (or
// another agent, or a webhook) wrote. They are lower-trust input in exactly the
// sense internal/untrusted defines: the preamble already names "issue bodies"
// as content that may try to instruct the model.
var fencedIssueFields = []string{"title", "description"}

// fenceIssueText wraps every free-text field of an issue payload in the
// nonce-delimited <untrusted source="issue" …> block from internal/untrusted,
// so text an agent READS off the board arrives as data rather than as prompt.
//
// This is the same chokepoint the orchestrator uses when it interpolates a
// mission description or a mission comment into a prompt (mission_tasks.go) —
// not a second mechanism. The difference is only WHERE the content enters the
// model's context: on those paths crewshipd assembles the prompt and can fence
// it there, whereas an agent that curls /issues pastes the response into its
// own context with nothing in between. The sidecar is that "in between".
//
// Applied to both shapes the internal API returns: a JSON array (list) and a
// JSON object (single issue, or an error body — which has neither field and is
// passed through untouched). A payload that does not parse is returned as-is:
// this is a defence-in-depth wrapper, and swallowing an unexpected upstream
// response would be a worse failure than an unfenced one.
func fenceIssueText(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	switch {
	case strings.HasPrefix(trimmed, "["):
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return raw
		}
		for i := range items {
			fenceIssueObject(items[i])
		}
		out, err := json.Marshal(items)
		if err != nil {
			return raw
		}
		return out
	case strings.HasPrefix(trimmed, "{"):
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return raw
		}
		fenceIssueObject(obj)
		out, err := json.Marshal(obj)
		if err != nil {
			return raw
		}
		return out
	default:
		return raw
	}
}

// fencedCommentFields is fencedIssueFields' sibling for one comment row —
// "body" is the free-text field an issue comment's author (human, agent, or
// a webhook-authored bot) wrote.
var fencedCommentFields = []string{"body"}

// fenceCommentText is fenceIssueText's sibling for GET /issue/{id}/comments
// (the sidecar comment-READ verb, PRD-ISSUES-AND-ROUTINES-2026 §11.1, work
// package B5, #2345): the internal API's ListComments always returns a JSON
// array, never a bare object, so this only needs that one shape — but still
// falls through to the raw payload on anything that doesn't parse as one,
// the same defence-in-depth fenceIssueText applies to an unexpected upstream
// response.
func fenceCommentText(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimLeft(string(raw), " \t\r\n")
	if !strings.HasPrefix(trimmed, "[") {
		return raw
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	for i := range items {
		fenceObjectFields(items[i], fencedCommentFields)
	}
	out, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	return out
}

// fenceIssueObject rewrites the free-text fields of one issue object in place.
// Non-string values (a JSON null description, say) are left alone.
func fenceIssueObject(obj map[string]json.RawMessage) {
	fenceObjectFields(obj, fencedIssueFields)
}

// fenceObjectFields is fenceIssueObject generalized to an arbitrary field
// list, shared with fenceCommentText — same nonce-fence, same "issue"
// source label (a comment IS content read off the issue board, the same
// ingress class fenceIssueText already names).
func fenceObjectFields(obj map[string]json.RawMessage, fields []string) {
	for _, field := range fields {
		raw, present := obj[field]
		if !present {
			continue
		}
		var text string
		if err := json.Unmarshal(raw, &text); err != nil || text == "" {
			continue
		}
		wrapped, err := json.Marshal(untrusted.Wrap("issue", text))
		if err != nil {
			continue
		}
		obj[field] = wrapped
	}
}
