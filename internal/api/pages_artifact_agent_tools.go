package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// RestoreArtifactAgent shares the human restore's retained-source, manifest,
// CAS, quota and receipt transaction. artifactPage and the final writer fence
// independently check the authenticated run's current authority and policy.
func (h *PageHandler) RestoreArtifactAgent(w http.ResponseWriter, r *http.Request) {
	if checked, ok := h.artifactAgentRequest(w, r); ok {
		h.writeArtifact(w, checked, true)
	}
}

// ListArtifactsAgent lists only explicit delegations for the authenticated run.
// The cursor advances over a bounded candidate scan; revoked rows are omitted.
func (h *PageHandler) ListArtifactsAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	offset := 0
	for key, values := range r.URL.Query() {
		if key == "workspace_id" && len(values) == 1 && values[0] == WorkspaceIDFromContext(r.Context()) {
			continue
		}
		if key != "cursor" || len(values) != 1 {
			replyError(w, 400, "only one cursor query is supported")
			return
		}
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 1000000 {
			replyError(w, 400, "invalid cursor")
			return
		}
		offset = n
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	a := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
	if err := h.checkArtifactRunPrincipal(ctx, a); err != nil {
		replyError(w, 403, err.Error())
		return
	}
	viewer, err := h.loadViewer(ctx, a.WorkspaceID, a.HumanUserID)
	if err != nil {
		h.boardError(w, err)
		return
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.slug,p.name,ar.head_rev FROM page_artifact_run_delegations d JOIN pages p ON p.id=d.page_id JOIN page_artifacts ar ON ar.page_id=p.id WHERE d.run_id=? AND d.granted_by_user_id=? AND p.workspace_id=? ORDER BY p.id LIMIT 101 OFFSET ?`, a.RunID, a.HumanUserID, a.WorkspaceID, offset)
	if err != nil {
		h.boardError(w, err)
		return
	}
	type candidate struct {
		id, slug, title string
		head            int64
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.slug, &c.title, &c.head); err != nil {
			break
		}
		candidates = append(candidates, c)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		h.boardError(w, err)
		return
	}
	hasMore := len(candidates) > 100
	if hasMore {
		candidates = candidates[:100]
	}
	items := []map[string]any{}
	for _, c := range candidates {
		authority := *a
		if h.checkArtifactRun(ctx, c.id, &authority) != nil {
			continue
		}
		rec, err := h.loadPage(ctx, a.WorkspaceID, c.slug)
		if err != nil {
			continue
		}
		visible, err := h.canSeePage(ctx, a.WorkspaceID, rec, nil, viewer)
		if err != nil {
			h.boardError(w, err)
			return
		}
		if !visible {
			continue
		}
		items = append(items, map[string]any{"page_slug": c.slug, "title": c.title, "head_rev": c.head})
	}
	next := ""
	if hasMore {
		next = strconv.Itoa(offset + len(candidates))
	}
	writeJSON(w, 200, map[string]any{"artifacts": items, "has_more": hasMore, "next_cursor": next})
}

const maxArtifactDiffSourceBytes = 256 << 10

// DiffArtifactAgent returns a linear replacement hunk for exact retained
// revisions. Oversized sources are refused as a whole; no partial diff exists.
func (h *PageHandler) DiffArtifactAgent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	r = checked
	for key, values := range r.URL.Query() {
		if key == "workspace_id" && len(values) == 1 && values[0] == WorkspaceIDFromContext(r.Context()) {
			continue
		}
		if (key != "from_revision" && key != "to_revision") || len(values) != 1 {
			replyError(w, 400, "from_revision and to_revision required once")
			return
		}
	}
	from, e1 := strconv.ParseInt(r.URL.Query().Get("from_revision"), 10, 64)
	to, e2 := strconv.ParseInt(r.URL.Query().Get("to_revision"), 10, 64)
	if e1 != nil || e2 != nil || from < 1 || to < 1 {
		replyError(w, 400, "positive from_revision and to_revision required")
		return
	}
	release, ok := acquireArtifactUpload(w, r)
	if !ok {
		return
	}
	defer release()
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.boardError(w, err)
		return
	}
	defer tx.Rollback()
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, ok := h.artifactPage(w, r, false)
	if !ok {
		return
	}
	data := make([][]byte, 2)
	digests := make([]string, 2)
	for i, rev := range []int64{from, to} {
		var recorded sql.NullString
		err := tx.QueryRowContext(r.Context(), `SELECT sha256,manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=?`, rec.ID, rev).Scan(&digests[i], &recorded)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, 404, "artifact revision not found")
			return
		}
		if err != nil {
			h.boardError(w, err)
			return
		}
		// Match GetArtifact's immutable-blob read: never wait on a blob writer
		// lock while holding a SQL read transaction. A concurrent GC race is
		// an explicit integrity/unavailable response, never a partial diff.
		content, err := readAttachmentBlob(h.artifactStoragePath, WorkspaceIDFromContext(r.Context()), digests[i])
		if err != nil || attachmentDigest(content) != digests[i] {
			replyError(w, 503, "artifact content failed integrity verification")
			return
		}
		if len(content) > maxArtifactDiffSourceBytes {
			artifactCommentError(w, 413, "diff_source_too_large", "Diff requires each HTML source within 256 KiB; read both exact authorized revisions and compare them locally. No partial diff was produced.")
			return
		}
		if _, _, err := resolveArtifactManifest(recorded, content, digests[i]); err != nil {
			replyError(w, 503, "artifact manifest failed integrity verification")
			return
		}
		data[i] = content
	}
	before, after := strings.Split(string(data[0]), "\n"), strings.Split(string(data[1]), "\n")
	prefix := 0
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(before)-prefix && suffix < len(after)-prefix && before[len(before)-1-suffix] == after[len(after)-1-suffix] {
		suffix++
	}
	removed := append([]string{}, before[prefix:len(before)-suffix]...)
	added := append([]string{}, after[prefix:len(after)-suffix]...)
	writeJSON(w, 200, map[string]any{"from_revision": from, "to_revision": to, "from_sha256": digests[0], "to_sha256": digests[1], "equal": bytes.Equal(data[0], data[1]), "prefix_lines": prefix, "suffix_lines": suffix, "removed_lines": removed, "added_lines": added})
}
