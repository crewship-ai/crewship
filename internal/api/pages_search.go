package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/pagesearch"
)

const maxNativeSearchResults = 50

// NativePageSearchMatch contains only a currently readable native Page. There
// are no folder paths, ancestor labels, global scores or hidden-source counts.
type NativePageSearchMatch struct {
	PageID           string `json:"page_id"`
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	ContentType      string `json:"content_type"`
	SchemaVersion    int    `json:"schema_version"`
	SourceRevision   int64  `json:"source_revision"`
	SourceGeneration int64  `json:"source_generation"`
	Snippet          string `json:"snippet"`
}
type NativePageSearchResponse struct {
	Scope   string                  `json:"scope"`
	Matches []NativePageSearchMatch `json:"matches"`
	HasMore bool                    `json:"has_more"`
	Cursor  string                  `json:"cursor,omitempty"`
}

func (h *PageHandler) initNativeSearch() {
	h.nativeSearchInit.Do(func() {
		h.nativeSearchSlots = make(chan struct{}, 2)
		h.nativeSearchCursors = pagesearch.NewCursorStore()
	})
}

func nativeSearchCursorBinding(r *http.Request, query pagesearch.Query) (pagesearch.CursorBinding, error) {
	user := UserFromContext(r.Context())
	if user == nil {
		return pagesearch.CursorBinding{}, errNativeSearchAuthentication
	}
	var identity string
	switch AuthKindFromContext(r.Context()) {
	case AuthKindSession:
		if user.SessionID == "" {
			return pagesearch.CursorBinding{}, errNativeSearchAuthentication
		}
		identity = "session:" + user.SessionID
	case AuthKindCLIToken:
		token := extractToken(r)
		if token == "" {
			return pagesearch.CursorBinding{}, errNativeSearchAuthentication
		}
		identity = "cli:" + token
	default:
		return pagesearch.CursorBinding{}, errNativeSearchAuthentication
	}
	sum := sha256.Sum256([]byte(identity))
	return pagesearch.CursorBinding{ViewerID: user.ID, CredentialHash: hex.EncodeToString(sum[:]), WorkspaceID: WorkspaceIDFromContext(r.Context()), QueryHash: query.Fingerprint()}, nil
}

// SearchNativePageContent refuses incomplete authorized native coverage. It is
// current Docs/Slides plain-text search, not all-Page fulltext or a backlink graph.
func (h *PageHandler) SearchNativePageContent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	values := r.URL.Query()
	if len(values["q"]) != 1 || len(values["cursor"]) > 1 || len(values["limit"]) > 1 {
		replyError(w, 400, "one bounded native text query required")
		return
	}
	query, err := pagesearch.ParseQuery(values.Get("q"))
	if err != nil {
		replyError(w, 400, "bounded nonempty native text query required")
		return
	}
	limit := maxNativeSearchResults
	if raw := values.Get("limit"); len(values["limit"]) != 0 {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > maxNativeSearchResults {
			replyError(w, 400, "native search limit must be between 1 and 50")
			return
		}
	}
	tx, err := h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.replyNativeSearchError(w, err)
		return
	}
	candidates, err := h.nativeSearchCandidates(r, tx)
	if err != nil {
		_ = tx.Rollback()
		h.replyNativeSearchError(w, err)
		return
	}
	binding, err := nativeSearchCursorBinding(r, query)
	if err != nil {
		_ = tx.Rollback()
		h.replyNativeSearchError(w, err)
		return
	}
	h.initNativeSearch()
	after := ""
	if cursor := values.Get("cursor"); cursor != "" {
		after, err = h.nativeSearchCursors.Resolve(cursor, binding)
	} else if len(values["cursor"]) != 0 {
		err = pagesearch.ErrCursor
	}
	_ = tx.Rollback()
	if err != nil {
		h.replyNativeSearchError(w, err)
		return
	}
	// Canonical materialization may perform IPC. No database transaction survives
	// this phase; previous candidate admission is never final response authority.
	for _, candidate := range candidates {
		if err = h.ensureNativeSearchProjection(ctx, r, candidate.PageID); err != nil {
			h.replyNativeSearchError(w, err)
			return
		}
	}
	tx, err = h.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		h.replyNativeSearchError(w, err)
		return
	}
	defer tx.Rollback()
	// Recompute ALL authorized native candidates after rebuild, before matching,
	// ranking or pagination. Newly visible/stale sources must be ready too.
	candidates, err = h.nativeSearchCandidates(r, tx)
	if err != nil {
		h.replyNativeSearchError(w, err)
		return
	}
	response := NativePageSearchResponse{Scope: "native-docs-slides", Matches: []NativePageSearchMatch{}}
	store := pagesearch.NewStore(h.db)
	for _, candidate := range candidates {
		guard := h.nativeSearchGuard(r, candidate.PageID)
		source, err := guard(ctx, tx)
		if err != nil {
			h.replyNativeSearchError(w, err)
			return
		}
		projection, err := store.Load(ctx, tx, source, guard)
		if err != nil {
			h.replyNativeSearchError(w, err)
			return
		}
		if candidate.PageID <= after {
			continue
		}
		snippet, matched, err := query.Match(projection)
		if err != nil {
			h.replyNativeSearchError(w, err)
			return
		}
		if !matched {
			continue
		}
		if len(response.Matches) == limit {
			response.HasMore = true
			continue
		}
		var name sql.NullString
		// Bound response metadata bytes too. No hidden names were loaded during the
		// candidate phase and no ancestor metadata is returned in search results.
		if err = tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(name AS BLOB))<=4096 THEN name ELSE NULL END FROM pages WHERE workspace_id=? AND id=?`, source.WorkspaceID, source.PageID).Scan(&name); err != nil {
			h.replyNativeSearchError(w, err)
			return
		}
		if !name.Valid {
			h.replyNativeSearchError(w, pagesearch.ErrBudget)
			return
		}
		response.Matches = append(response.Matches, NativePageSearchMatch{PageID: source.PageID, Slug: candidate.Slug, Name: name.String, ContentType: source.Type, SchemaVersion: source.Schema, SourceRevision: source.Revision, SourceGeneration: source.Generation, Snippet: snippet})
	}
	if err = ctx.Err(); err != nil {
		h.replyNativeSearchError(w, err)
		return
	}
	if response.HasMore {
		response.Cursor, err = h.nativeSearchCursors.Issue(binding, response.Matches[len(response.Matches)-1].PageID)
		if err != nil {
			h.replyNativeSearchError(w, err)
			return
		}
	}
	// Keep the final authority/source snapshot open through response encoding.
	writeJSON(w, 200, response)
}

func (h *PageHandler) replyNativeSearchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errContentCredentialScope):
		replyError(w, 403, "current pages:read scope required")
	case errors.Is(err, errNativeSearchAuthentication):
		replyError(w, 401, "current human credential required")
	case errors.Is(err, errNativeSearchMembership):
		replyError(w, 403, "current trusted workspace membership required")
	case errors.Is(err, pagesearch.ErrCursor):
		replyError(w, 400, "search continuation unavailable; run the search again")
	case errors.Is(err, errNativeSearchCandidates), errors.Is(err, pagesearch.ErrBudget):
		replyError(w, 422, "native text search exceeds supported content limits")
	case errors.Is(err, pagesearch.ErrUnsupported):
		replyError(w, 422, "native Page format is not supported by text search yet")
	case errors.Is(err, pagesearch.ErrNotFound), errors.Is(err, pagesearch.ErrSourceChanged), errors.Is(err, pagesearch.ErrInvalid), errors.Is(err, errNativeSearchUnavailable), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		replyError(w, 503, "native text search is not ready or changed; retry")
	default:
		replyInternalError(w, h.logger, "native content search", err)
	}
}
