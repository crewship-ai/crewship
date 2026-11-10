package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/documents"
)

// documentServiceCall carries the caller's credential to the independent engine.
// The engine calls the Go authority, which rechecks it under the SQL writer fence.
func (r *Router) documentServiceCall(ctx context.Context, workspace, bearer, page, operation string, body any) ([]byte, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 500, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docs/v1/documents/"+page+"/"+operation, bytes.NewReader(payload))
	if err != nil {
		return nil, 500, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearer)
	req.Header.Set("X-Workspace-ID", workspace)
	req.Header.Set("X-Crewship-Docs-Service", r.documentsServiceSecret)
	response, err := r.documentsClient.Do(req)
	if err != nil {
		return nil, 503, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, 502, io.ErrUnexpectedEOF
	}
	return raw, response.StatusCode, nil
}

func (h *PageHandler) documentPage(w http.ResponseWriter, r *http.Request) (*pageRecord, bool) {
	if h.documentCall == nil {
		replyError(w, 503, "Docs are not enabled on this server")
		return nil, false
	}
	// Custody is a read audience, never permission for initialize/patch.
	load := h.loadPage
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		load = h.loadPageForRead
	}
	rec, err := load(r.Context(), WorkspaceIDFromContext(r.Context()), r.PathValue("slug"))
	if err != nil {
		replyError(w, 404, "document page not found")
		return nil, false
	}
	return rec, true
}
func (h *PageHandler) GetDocument(w http.ResponseWriter, r *http.Request) {
	h.serveDocument(w, r, false)
}
func (h *PageHandler) MutateDocument(w http.ResponseWriter, r *http.Request) {
	h.serveDocument(w, r, true)
}
func (h *PageHandler) serveDocument(w http.ResponseWriter, r *http.Request, mutate bool) {
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "human document credentials required")
		return
	}
	if !mutate {
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			replyError(w, 400, "invalid Document read query")
			return
		}
		if query.Has("revision") {
			replyError(w, 400, "selected revision reads are not enabled")
			return
		}
	}
	if !mutate && strings.HasSuffix(r.URL.Path, "/document/export") {
		if format := r.URL.Query().Get("format"); len(r.URL.Query()["format"]) != 1 || (format != "markdown" && format != "md" && format != "docx") {
			replyError(w, 400, "format must be markdown or docx")
			return
		}
	}
	if r.URL.Query().Has("block_id") {
		replyError(w, 400, "selected block reads are not enabled")
		return
	}
	rec, ok := h.documentPage(w, r)
	if !ok {
		return
	}
	if !h.auditPrivateOwnerAccess(w, r, rec) {
		return
	}
	operation := "read"
	if strings.HasSuffix(r.URL.Path, "/document/comments") {
		operation = "comments"
	}
	exporting := !mutate && strings.HasSuffix(r.URL.Path, "/document/export")
	if exporting {
		// Export is the same confirmed read as GET /document (the engine reads
		// state through the private authority with this caller's credential,
		// including private-custody checks and audit), serialized by the engine.
		operation = "export"
	}
	var body any = map[string]any{}
	exportDOCX := exporting && r.URL.Query().Get("format") == "docx"
	if exportDOCX {
		body = map[string]any{"format": "docx"}
	}
	if exporting {
		var prepared bool
		body, prepared = h.documentExportRequest(w, r, rec, body.(map[string]any))
		if !prepared {
			return
		}
	}
	if mutate {
		operation = r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		if operation == "comments" {
			operation = "comment"
		}
		if operation != "initialize" && operation != "patch" && operation != "upgrade" && operation != "comment" && operation != "resolve" {
			replyError(w, 404, "unknown document operation")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		decoder := json.NewDecoder(r.Body)
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil || decoder.Decode(new(any)) != io.EOF {
			replyError(w, 400, "one bounded JSON object required")
			return
		}
		object := bytes.TrimSpace(raw)
		if len(object) == 0 || object[0] != '{' {
			replyError(w, 400, "one bounded JSON object required")
			return
		}
		body = raw
	}
	bearer := "Bearer " + extractToken(r)
	raw, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), bearer, rec.ID, operation, body)
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		replyError(w, 503, "document engine unavailable; changes are not acknowledged")
		return
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil || value == nil {
		replyError(w, 502, "invalid document engine response")
		return
	}
	if exportDOCX {
		encoded, ok := value["docx"].(string)
		file, err := base64.StdEncoding.DecodeString(encoded)
		if !ok || len(value) != 1 || err != nil || len(file) > documentDOCXExportBytes || !bytes.HasPrefix(file, []byte("PK\x03\x04")) {
			replyError(w, 502, "invalid document engine response")
			return
		}
		w.Header().Set("Content-Type", documentDOCXMediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": documentExportFilename(rec.Slug, "docx")}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(200)
		_, _ = w.Write(file)
		return
	}
	if exporting {
		markdown, ok := value["markdown"].(string)
		if !ok || len(value) != 1 {
			replyError(w, 502, "invalid document engine response")
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": documentExportFilename(rec.Slug, "md")}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, markdown)
		return
	}
	if operation == "read" || operation == "comments" {
		value["page_id"] = rec.ID
		user := UserFromContext(r.Context())
		workspace, role := WorkspaceIDFromContext(r.Context()), RoleFromContext(r.Context())
		if rec.PrivateAudience && user != nil {
			var err error
			if role, err = h.liveContentHumanRole(r.Context(), workspace, user.ID); err != nil {
				role = ""
			}
		}
		canWrite := user != nil && role != "" && h.mayEditSpec(r.Context(), workspace, user.ID, role, rec)
		// The same rule the authority applies to every comment write: Comment
		// authority, and on private output only its custodian.
		canComment := user != nil && role != "" && h.mayCommentPage(r.Context(), workspace, user.ID, role, rec) && (!rec.PrivateAudience || rec.OwnerUserID == user.ID)
		// A restricted member's comment and proposal routes are outside its
		// route allowlist, so it must not be offered those controls.
		restricted := true
		if user != nil {
			if found, err := (access.Store{DB: h.db}).HasRestrictedMembership(r.Context(), user.ID); err == nil {
				restricted = found
			}
		}
		if restricted {
			canComment = false
		}
		if operation == "read" {
			value["can_write"] = canWrite
			// What this Doc's schema supports (#5233), and whether this
			// reader is offered agent proposals and their review. This states
			// capabilities; accept and reject still need Edit.
			schema, _ := value["schema"].(float64)
			value["capabilities"] = documents.SchemaCapabilities(int(schema))
			value["proposals"] = documents.SchemaAllows(int(schema), documents.CapPropose) && !restricted
		}
		value["can_comment"] = canComment
		value["can_resolve"] = canComment
	}
	writeJSON(w, 200, value)
}

// The engine's DOCX delivery limit (tools/docs-service MAX_DOCX_BYTES, just
// under 3 MiB): its base64 {"docx":...} reply must fit the 4 MiB engine read.
const documentDOCXExportBytes = ((4 << 20) - 64) / 4 * 3

const documentDOCXMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// documentExportFilename names a download after the artifact slug, reduced to
// a safe ASCII file name.
func documentExportFilename(slug, extension string) string {
	var b strings.Builder
	for _, c := range slug {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		}
		if b.Len() >= 100 {
			break
		}
	}
	if b.Len() == 0 {
		b.WriteString("document")
	}
	return b.String() + "." + extension
}

// Resolve labels from the exact confirmed Page revision, with the same reader
// checks used by the editor. A concurrent edit/restore causes the engine to
// refuse the export rather than combine labels from one head with another.
func (h *PageHandler) documentExportRequest(w http.ResponseWriter, r *http.Request, rec *pageRecord, body map[string]any) (map[string]any, bool) {
	raw, status, err := h.documentCall(r.Context(), WorkspaceIDFromContext(r.Context()), "Bearer "+extractToken(r), rec.ID, "read", map[string]any{})
	if err != nil {
		replyError(w, 503, "document engine unavailable")
		return nil, false
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return nil, false
	}
	var head struct {
		Rev     *int64          `json:"rev"`
		Epoch   string          `json:"epoch"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &head) != nil || head.Rev == nil || *head.Rev < 0 || head.Epoch == "" || len(head.Content) == 0 {
		replyError(w, 502, "invalid document engine response")
		return nil, false
	}
	var content any
	if json.Unmarshal(head.Content, &content) != nil {
		replyError(w, 502, "invalid document engine response")
		return nil, false
	}
	seen, ids := map[string]bool{}, []string{}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if node["type"] == "mention" {
				if attrs, ok := node["attrs"].(map[string]any); ok {
					if id, ok := attrs["userId"].(string); ok && documentPersonID.MatchString(id) && !seen[id] {
						seen[id] = true
						ids = append(ids, id)
					}
				}
			}
			if children, ok := node["content"].([]any); ok {
				for _, child := range children {
					visit(child)
				}
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(content)
	if len(ids) > 1000 {
		replyError(w, 422, "too many distinct people for export")
		return nil, false
	}
	people := map[string]string{}
	for start := 0; start < len(ids); start += documentPeopleIDLimit {
		end := start + documentPeopleIDLimit
		if end > len(ids) {
			end = len(ids)
		}
		lookup := r.Clone(r.Context())
		lookup.URL = new(url.URL)
		*lookup.URL = *r.URL
		query := url.Values{"ids": {strings.Join(ids[start:end], ",")}, "workspace_id": {WorkspaceIDFromContext(r.Context())}}
		lookup.URL.RawQuery = query.Encode()
		lookup.SetPathValue("slug", "@"+rec.ID)
		answered := false
		h.serveDocumentPeople(w, lookup, func(found []documentPerson) {
			answered = true
			for _, person := range found {
				people[person.ID] = person.Name
			}
		})
		if !answered {
			return nil, false
		}
	}
	body["expected_rev"], body["expected_epoch"] = *head.Rev, head.Epoch
	if len(people) > 0 {
		body["people"] = people
	}
	return body, true
}
