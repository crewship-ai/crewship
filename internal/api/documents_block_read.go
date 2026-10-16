package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/pagesearch"
)

// authenticateAgent is the existing private authority's run-caller wrapper.
// It returns false for human requests, whose existing route middleware remains
// responsible for authentication. Both call paths use authorize inside SQL.
func (h *documentAuthority) authenticateAgent(w http.ResponseWriter, r *http.Request, operation http.Handler) bool {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer agtv2.") {
		return false
	}
	// Resolve the transport's crew from the stored principal, then let the
	// existing run-capability verifier authenticate its HMAC and liveness.
	fields := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(fields) != 5 {
		replyError(w, 403, "invalid run capability")
		return true
	}
	run, e := base64.RawURLEncoding.DecodeString(fields[3])
	if e != nil {
		replyError(w, 403, "invalid run capability")
		return true
	}
	var workspace, crew string
	if h.pages.db.QueryRowContext(r.Context(), `SELECT workspace_id,crew_id FROM artifact_run_principals WHERE run_id=?`, string(run)).Scan(&workspace, &crew) != nil || workspace != r.Header.Get("X-Workspace-ID") {
		replyError(w, 403, "invalid run capability")
		return true
	}
	ctx := context.WithValue(r.Context(), ctxInternalTokenWS, workspace)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, crew)
	if checked, ok := h.pages.artifactAgentRequest(w, r.WithContext(ctx)); ok {
		operation.ServeHTTP(w, checked)
	}
	return true
}

const maxDocumentBlockTextBytes = 256 << 10

var documentBlockIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type DocumentBlockReadResponse struct {
	PageID     string               `json:"page_id"`
	Schema     int                  `json:"schema"`
	Rev        int64                `json:"rev"`
	Generation int64                `json:"generation"`
	Epoch      string               `json:"epoch"`
	Block      DocumentOutlineBlock `json:"block"`
}

func documentBlockSelector(r *http.Request) (string, bool, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false, err
	}
	values, present := query["block_id"]
	if !present {
		return "", false, nil
	}
	current := r.PathValue("checkpoint") == "" && (strings.HasSuffix(r.URL.Path, "/document") || strings.HasPrefix(r.URL.Path, "/api/v1/internal/documents/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/documents/"), "/"))
	if r.Method != http.MethodGet || !current || len(values) != 1 || !documentBlockIdentifier.MatchString(values[0]) {
		return "", true, errors.New("one valid block_id is supported only for current document GET")
	}
	workspace := WorkspaceIDFromContext(r.Context())
	if strings.HasPrefix(r.URL.Path, "/api/v1/internal/documents/") && InternalTokenWorkspaceFromContext(r.Context()) != "" {
		// Internal middleware injects its authenticated realm into this query.
		// The read adapter separately compares that realm to the run principal.
		workspace = InternalTokenWorkspaceFromContext(r.Context())
	}
	for key, values := range query {
		if key == "block_id" {
			continue
		}
		if key != "workspace_id" || len(values) != 1 || values[0] != workspace {
			return "", true, errors.New("unsupported block read query")
		}
	}
	return values[0], true, nil
}

func (h *PageHandler) readDocumentBlock(w http.ResponseWriter, r *http.Request, rec *pageRecord, target string) {
	authority := &documentAuthority{pages: h}
	transportWorkspace, transportCrew := "", ""
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer agtv2.") {
		transportWorkspace, transportCrew = InternalTokenWorkspaceFromContext(r.Context()), InternalTokenCrewFromContext(r.Context())
		fields := strings.Split(extractToken(r), ".")
		if !strings.HasPrefix(r.URL.Path, "/api/v1/internal/documents/") || transportWorkspace == "" || transportCrew == "" || WorkspaceIDFromContext(r.Context()) != transportWorkspace || len(fields) != 5 {
			replyError(w, 403, "document run transport does not match")
			return
		}
		run, err := base64.RawURLEncoding.DecodeString(fields[3])
		var principalWorkspace, principalCrew string
		if err != nil || h.db.QueryRowContext(r.Context(), `SELECT workspace_id,crew_id FROM artifact_run_principals WHERE run_id=?`, string(run)).Scan(&principalWorkspace, &principalCrew) != nil || principalWorkspace != transportWorkspace || principalCrew != transportCrew {
			replyError(w, 403, "document run transport does not match")
			return
		}
		// This header comes only from the authenticated internal crew context,
		// never the caller's query/header. Private service requests stay unchanged.
		r = r.Clone(r.Context())
		r.Header.Set("X-Workspace-ID", transportWorkspace)
	}
	operation := http.HandlerFunc(func(w http.ResponseWriter, checked *http.Request) {
		if transportWorkspace != "" {
			run, ok := checked.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
			if !ok || run.WorkspaceID != transportWorkspace || run.CrewID != transportCrew {
				replyError(w, 403, "document run transport does not match")
				return
			}
		}
		h.readAuthorizedDocumentBlock(w, checked, rec, target, authority)
	})
	if !authority.authenticateAgent(w, r, operation) {
		operation.ServeHTTP(w, r)
	}
}

func (h *PageHandler) readAuthorizedDocumentBlock(w http.ResponseWriter, r *http.Request, rec *pageRecord, target string, authority *documentAuthority) {
	key := documents.Key{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: rec.ID}
	raw, status, err := h.documentCall(r.Context(), key.WorkspaceID, "Bearer "+extractToken(r), key.PageID, "read", map[string]any{})
	w.Header().Set("Cache-Control", "no-store")
	if err != nil {
		if status == 502 {
			replyError(w, 502, "bounded document read unavailable; the complete engine response must fit 4 MiB")
		} else {
			replyError(w, 503, "document engine unavailable")
		}
		return
	}
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
		return
	}
	var read struct {
		Schema     int                    `json:"schema"`
		Rev        int64                  `json:"rev"`
		Generation int64                  `json:"generation"`
		Epoch      string                 `json:"epoch"`
		Blocks     []pagesearch.BlockHash `json:"blocks"`
		Outline    []DocumentOutlineBlock `json:"outline"`
	}
	if len(raw) > 4<<20 {
		replyError(w, 502, "bounded document read unavailable; the complete engine response must fit 4 MiB")
		return
	}
	if !utf8.Valid(raw) || json.Unmarshal(raw, &read) != nil || read.Schema < 1 || read.Schema > documents.LatestSchema || read.Rev < 0 || read.Generation < 0 || read.Epoch == "" || len(read.Epoch) > 512 {
		replyError(w, 502, "invalid document engine response")
		return
	}
	hash, err := pagesearch.DocumentHash(read.Blocks)
	if err != nil || len(read.Blocks) != len(read.Outline) {
		replyError(w, 502, "invalid document engine projection")
		return
	}
	known := map[string]string{}
	for _, block := range read.Blocks {
		known[block.ID] = block.Hash
	}
	seen := map[string]bool{}
	var selected *DocumentOutlineBlock
	for i, block := range read.Outline {
		expected, exists := known[block.ID]
		if seen[block.ID] || !exists || expected != block.Hash {
			replyError(w, 502, "invalid document engine outline")
			return
		}
		seen[block.ID] = true
		if block.ID == target {
			selected = &read.Outline[i]
		}
	}
	// Reauthorize and bind the entire immutable materialized source before any
	// selected text, missing-target or nontext outcome leaves this request.
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyError(w, 503, "document authority unavailable")
		return
	}
	defer tx.Rollback()
	if _, err = authority.authorize(r, key, false, false)(r.Context(), tx); err != nil {
		documentAuthorityError(w, err)
		return
	}
	var schema int
	var rev, generation int64
	var size int
	var persisted []byte
	err = tx.QueryRowContext(r.Context(), `SELECT schema_version,head_rev,generation,length(projection),CASE WHEN length(projection)<=? THEN projection ELSE NULL END FROM documents WHERE workspace_id=? AND page_id=?`, 4<<20, key.WorkspaceID, key.PageID).Scan(&schema, &rev, &generation, &size, &persisted)
	if errors.Is(err, sql.ErrNoRows) {
		documentAuthorityError(w, documents.ErrNotFound)
		return
	}
	if err != nil {
		replyError(w, 503, "document authority unavailable")
		return
	}
	var blocks []pagesearch.BlockHash
	if size != len(persisted) || !utf8.Valid(persisted) || json.Unmarshal(persisted, &blocks) != nil {
		replyError(w, 503, "document source unavailable")
		return
	}
	currentHash, err := pagesearch.DocumentHash(blocks)
	if err != nil || schema != read.Schema || rev != read.Rev || generation != read.Generation || currentHash != hash {
		documentAuthorityError(w, documents.ErrConflict)
		return
	}
	// The original identity is authoritative even if the slug was renamed or reused.
	var currentID string
	if err = tx.QueryRowContext(r.Context(), `SELECT id FROM pages WHERE workspace_id=? AND slug=?`, key.WorkspaceID, rec.Slug).Scan(&currentID); err != nil || currentID != key.PageID {
		documentAuthorityError(w, documents.ErrConflict)
		return
	}
	if err = tx.Commit(); err != nil {
		replyError(w, 503, "document authority unavailable")
		return
	}
	if selected == nil {
		replyError(w, 404, "document block not found")
		return
	}
	if selected.Type != "paragraph" && selected.Type != "heading" && !(selected.Type == "codeBlock" && read.Schema >= 3) {
		replyError(w, 422, "target is not a supported text block")
		return
	}
	if !utf8.ValidString(selected.Text) || len(selected.Text) > maxDocumentBlockTextBytes {
		replyError(w, 422, "document block text exceeds 256 KiB; content is not truncated")
		return
	}
	writeJSON(w, 200, DocumentBlockReadResponse{key.PageID, read.Schema, read.Rev, read.Generation, read.Epoch, *selected})
}
