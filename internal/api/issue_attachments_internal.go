package api

// Attachments on an issue — the AGENT surface (#1768, item 7).
//
// This is the half that makes the feature real. An attachment an agent cannot
// read is decoration: the whole reason a person drops a stack trace, a failing
// log or a screenshot onto an issue is so the agent working that issue can look
// at it. Before this file the agent's issue surface (issues_internal.go, and its
// sidecar twin) could read a title, a description, comments and linked pull
// requests, and nothing else.
//
// Routes, reached only by the sidecar's X-Internal-Token:
//
//	GET  /api/v1/internal/issues/{identifier}/attachments
//	GET  /api/v1/internal/issues/{identifier}/attachments/{attachmentId}
//	POST /api/v1/internal/issues/{identifier}/attachments
//
// ── Everything here is untrusted content ───────────────────────────────────
//
// An uploaded file is attacker-controlled by construction: on a self-hosted
// instance anyone who can comment on an issue can attach to it, and the content
// lands directly in an agent's context. That is the ingress prompt-injection
// surface internal/untrusted exists for (OWASP LLM01), so both the FILENAME and
// the CONTENT go through the fence with their own source label before they leave
// this handler. The filename matters as much as the body: "ignore previous
// instructions.txt" is a shorter and likelier payload than the file itself.
//
// The fence is applied HERE rather than in the sidecar because this handler is
// the one that decides what an agent is shown; the sidecar's fenceIssueText
// wraps the issue payload and never sees an attachment body.

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/untrusted"
	"github.com/crewship-ai/crewship/internal/ws"
)

// agentAttachmentTextBudget bounds how much text is inlined into an agent's
// context in one read.
//
// 128 KiB is roughly 30k tokens of prose — already more than most models will
// use well, and far past the point where an agent should be reading a whole file
// rather than the part it needs. The response says `truncated` when it bites, so
// the agent knows it is looking at a prefix rather than silently reasoning about
// a file it has only partly seen. That flag is the point: a silent truncation is
// worse than a refusal.
const agentAttachmentTextBudget = 128 << 10

// agentAttachmentBinaryBudget bounds base64-inlined binary.
//
// Much smaller than the text budget, because base64 costs 4 tokens for every 3
// bytes and a model reading it learns almost nothing. It is here so a small
// screenshot or a short capture is reachable at all; anything bigger is a
// deliberate refusal that names the size rather than a 20 MiB blob dropped into
// a prompt.
const agentAttachmentBinaryBudget = 512 << 10

// agentAttachmentUploadBytes caps what an agent may attach in one call.
//
// Lower than the human 25 MiB on purpose. The agent door takes base64 inside a
// JSON body, so the bytes are buffered as the encoded body, as the decoded
// slice, and again as the blob — three copies, on the request path of a process
// that also holds the crew's credentials. A human upload is multipart and
// streams. The number is what the transport can carry safely, not a judgement
// about what agents deserve to attach.
const agentAttachmentUploadBytes = 6 << 20

// InternalAttachmentHandler serves the agent-facing attachment surface.
//
// It embeds the public handler's write path rather than reimplementing it: the
// sanitisation, the type allowlist, the content addressing and the
// de-duplication must be identical on both doors or the guarantees are only true
// of whichever door someone remembered.
type InternalAttachmentHandler struct {
	pub    *AttachmentHandler
	db     *sql.DB
	logger *slog.Logger
}

// NewInternalAttachmentHandler wires the internal twin over a public handler.
func NewInternalAttachmentHandler(pub *AttachmentHandler) *InternalAttachmentHandler {
	return &InternalAttachmentHandler{pub: pub, db: pub.db, logger: pub.logger}
}

// SetHub is here so the router can share the public handler's hub; the events
// emitter reads it off pub.
func (h *InternalAttachmentHandler) SetHub(hub *ws.Hub) { h.pub.hub = hub }

// agentAttachment is one attachment as an AGENT sees it.
//
// Filename is the FENCED display name — a nonce-delimited <untrusted> block, not
// a bare string. It is not called `filename_fenced`: naming it plainly is what
// stops a caller from reaching for the "real" one, because there isn't one on
// this surface.
type agentAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	CreatedAt   string `json:"created_at"`
	// UploadedBy is "user" or "agent" — the KIND, not the identity. An agent
	// deciding how much to trust a file wants to know a human put it there;
	// it does not need the uploader's name, and a display name is one more
	// attacker-chosen string to fence.
	UploadedBy string `json:"uploaded_by"`
}

// agentAttachmentContent is the read response.
type agentAttachmentContent struct {
	agentAttachment
	// Encoding is "text" or "base64".
	Encoding string `json:"encoding"`
	// Content is the fenced text (encoding="text") or raw base64
	// (encoding="base64"). Base64 is deliberately NOT fenced: the fence's
	// contract is "this region is data, not instructions", and an agent that
	// asked for bytes is going to decode and write them to a file, where a
	// wrapper it has to strip first is a bug waiting to happen. What protects
	// that path is the size ceiling and the fact the agent chose to decode.
	Content string `json:"content"`
	// Truncated says the content is a prefix. Always present so a reader that
	// forgets to check gets `false` rather than nothing.
	Truncated bool `json:"truncated"`
}

func toAgentAttachment(a attachmentResponse) agentAttachment {
	by := "user"
	if a.UploadedByAgentID != nil && *a.UploadedByAgentID != "" {
		by = "agent"
	}
	return agentAttachment{
		ID: a.ID,
		// The filename is content someone chose. Fence it.
		Filename:    untrusted.Wrap("attachment_filename", a.Filename),
		ContentType: a.ContentType,
		SizeBytes:   a.SizeBytes,
		SHA256:      a.SHA256,
		CreatedAt:   a.CreatedAt,
		UploadedBy:  by,
	}
}

// ── List — GET /api/v1/internal/issues/{identifier}/attachments ────────────

// List returns the issue's attachments as metadata only.
//
// Metadata first, content on request, rather than one call that returns
// everything: an issue with four log files would otherwise put all four into the
// agent's context whether or not it needed any of them, and the agent could not
// decline. Listing is cheap; reading is a decision.
func (h *InternalAttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	rows, err := listIssueAttachments(r, h.db, missionID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "internal list attachments", err)
		return
	}
	out := make([]agentAttachment, 0, len(rows))
	for _, a := range rows {
		out = append(out, toAgentAttachment(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Read — GET …/attachments/{attachmentId} ────────────────────────────────

// Read returns one attachment's content, fenced and budgeted.
func (h *InternalAttachmentHandler) Read(w http.ResponseWriter, r *http.Request) {
	missionID, wsID, ok := h.resolveIssue(w, r)
	if !ok {
		return
	}
	att, err := h.pub.loadScoped(r, r.PathValue("attachmentId"), missionID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Attachment not found")
			return
		}
		internalError(w, r, h.logger, "internal load attachment", err)
		return
	}
	if h.pub.storagePath == "" {
		writeProblem(w, r, http.StatusServiceUnavailable, "Attachment storage is not configured on this instance")
		return
	}

	data, err := readAttachmentBlob(h.pub.storagePath, wsID, att.SHA256)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeProblem(w, r, http.StatusNotFound, "Attachment not found")
			return
		}
		internalError(w, r, h.logger, "internal read attachment blob", err)
		return
	}

	resp := agentAttachmentContent{agentAttachment: toAgentAttachment(att)}
	if attachmentIsText(att.ContentType) {
		resp.Encoding = "text"
		text := string(data)
		if len(text) > agentAttachmentTextBudget {
			// Cut on a rune boundary: a half UTF-8 sequence at the seam is a
			// replacement character in the model's context and, on a strict
			// consumer, a decode error.
			text = truncateUTF8(text, agentAttachmentTextBudget)
			resp.Truncated = true
		}
		// The content is the payload the fence exists for. source is
		// "attachment", a label WE chose — never anything from the file.
		resp.Content = untrusted.Wrap("attachment", text)
	} else {
		resp.Encoding = "base64"
		if len(data) > agentAttachmentBinaryBudget {
			data = data[:agentAttachmentBinaryBudget]
			resp.Truncated = true
		}
		resp.Content = base64.StdEncoding.EncodeToString(data)
	}
	writeJSON(w, http.StatusOK, resp)
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8ValidBoundary(s, n) {
		n--
	}
	return s[:n]
}

// utf8ValidBoundary reports whether index i is the start of a rune (or the end).
func utf8ValidBoundary(s string, i int) bool {
	if i <= 0 || i >= len(s) {
		return true
	}
	return s[i]&0xC0 != 0x80
}

// ── Attach — POST …/attachments ────────────────────────────────────────────

// Attach lets an agent attach a file it produced.
//
// The body is JSON with base64 content rather than multipart, because the
// sidecar's whole forwarding layer is JSON and a multipart passthrough would
// mean a second transport with its own body-cap, its own error shape and its own
// bugs, for the one route that needs it.
//
// The acting agent comes from the request body — but only because the SIDECAR
// put it there from the per-agent bearer token; the agent cannot influence it
// (see issue_attachments.go in internal/sidecar, which parses and ignores a
// body-supplied agent_id exactly as the comment and update verbs do). This
// handler is not reachable without the internal token, and the token is bound to
// a crew.
func (h *InternalAttachmentHandler) Attach(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID   string `json:"workspace_id"`
		AgentID       string `json:"agent_id"`
		Filename      string `json:"filename"`
		ContentBase64 string `json:"content_base64"`
	}
	if err := readJSONLimit(r, &req, agentAttachmentUploadBytes+(agentAttachmentUploadBytes/3)+1024); err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "Attachment body too large")
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}
	// The workspace arrives in the BODY, which requireInternal never parses — it
	// pins the query and injects the bound workspace there, which is what makes
	// the read routes below tenant-safe for free. A body-carried one is proven
	// here or not at all, exactly as every sibling internal write does
	// (issues_internal.go, missions_internal.go, keeper_request.go, …).
	//
	// Without it a holder of workspace A's internal token could name workspace B
	// and B's issue identifier: the blob landed in B's storage tree, the row in
	// B's table, and an `attachment_added` entry on B's issue timeline — which
	// notifies B's users. The agent_id check below does not catch it, because it
	// validates the id against the ATTACKER-SUPPLIED workspace. That the sidecar
	// happens to build its scope from IPC identity is a property of one caller,
	// not of this door.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	if strings.TrimSpace(req.Filename) == "" {
		writeProblem(w, r, http.StatusBadRequest, "filename is required")
		return
	}

	missionID, ok := h.resolveIssueIn(w, r, req.WorkspaceID)
	if !ok {
		return
	}
	if h.pub.storagePath == "" {
		writeProblem(w, r, http.StatusServiceUnavailable, "Attachment storage is not configured on this instance")
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.ContentBase64)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "content_base64 is not valid base64")
		return
	}
	if len(data) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "content_base64 is empty")
		return
	}
	if len(data) > agentAttachmentUploadBytes {
		writeProblem(w, r, http.StatusRequestEntityTooLarge,
			"Attachment larger than the agent upload limit")
		return
	}

	// The agent id is validated against the workspace before it is written, so a
	// sidecar bug (or a future caller) cannot attribute a file to an agent in
	// another tenant. An unresolvable id attaches the file with no agent
	// attribution rather than failing: the file is the thing the agent asked
	// for, and a wrong name is worse than none.
	agentID := req.AgentID
	if agentID != "" {
		var exists int
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM agents WHERE id = ? AND workspace_id = ?`, agentID, req.WorkspaceID).Scan(&exists)
		if exists == 0 {
			agentID = ""
		}
	}

	att, created, err := h.pub.attachBytes(r, req.WorkspaceID, missionID, req.Filename, data, "", agentID)
	if err != nil {
		h.pub.replyAttachError(w, r, err)
		return
	}
	if created {
		actorType, actorID := "agent", agentID
		if actorID == "" {
			actorType = "system"
		}
		h.pub.recordAttachmentEvent(r, missionID, req.WorkspaceID, actorType, actorID, actionAttachmentAdded, att)
		writeJSON(w, http.StatusCreated, toAgentAttachment(att))
		return
	}
	writeJSON(w, http.StatusOK, toAgentAttachment(att))
}

// ── plumbing ───────────────────────────────────────────────────────────────

// resolveIssue reads the workspace off the query string (the shape every other
// internal issue route uses — see InternalIssueHandler.Get) and resolves the
// identifier inside it.
//
// The QUERY is the safe place to read it from, and that is not incidental:
// requireInternal refuses a query workspace_id that disagrees with the token's
// binding and injects the bound one when it is omitted, so these two read routes
// are pinned by the middleware before the handler runs. It is the reason the
// tenancy fix on Attach is one assert on the body and not three on the query.
//
// Above that, the sidecar supplies it from its IPC identity rather than from the
// agent: issueScopeQuery in internal/sidecar sets it from s.ipc.WorkspaceID and
// the allowlisted forward drops any workspace_id the agent sent. An identifier
// that belongs to another tenant therefore resolves to nothing, and answers 404
// rather than a 403 that would confirm the issue exists.
func (h *InternalAttachmentHandler) resolveIssue(w http.ResponseWriter, r *http.Request) (missionID, wsID string, ok bool) {
	wsID = r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return "", "", false
	}
	missionID, ok = h.resolveIssueIn(w, r, wsID)
	return missionID, wsID, ok
}

// resolveIssueIn resolves {identifier} inside an explicit workspace.
//
// No crew_id predicate: the internal issue routes are already constrained to the
// token's crew upstream (effectiveCrewFilter, #1186) and the sidecar does not
// send one. Scoping by workspace is what makes this tenant-safe, and it is not
// optional — dropping it is the mutation that turns this route into a
// cross-tenant file reader.
func (h *InternalAttachmentHandler) resolveIssueIn(w http.ResponseWriter, r *http.Request, wsID string) (string, bool) {
	var missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		r.PathValue("identifier"), wsID).Scan(&missionID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return "", false
	}
	return missionID, true
}

// errBodyTooLarge is returned by readJSONLimit when the body exceeds its cap.
var errBodyTooLarge = errors.New("request body too large")

// readJSONLimit is readJSON with a caller-chosen ceiling, and — unlike readJSON
// — it can TELL a caller that the body was too large.
//
// readJSON truncates silently at 1 MiB: it reads through an io.LimitReader and
// hands the prefix to json.Unmarshal, so an oversized body comes back as
// "invalid JSON" (or, on an unlucky boundary, as a successfully parsed prefix).
// That is the wrong answer for an upload, where "too big" is the single most
// likely failure and the caller can act on it.
func readJSONLimit(r *http.Request, v interface{}, max int64) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > max {
		return errBodyTooLarge
	}
	return json.Unmarshal(body, v)
}
