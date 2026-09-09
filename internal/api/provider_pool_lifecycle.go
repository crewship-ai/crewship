package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/providerpool"
)

// Require one strong numeric ETag; wildcards cannot bypass stale-edit checks.
func providerPoolRevision(w http.ResponseWriter, r *http.Request) (int64, bool) {
	value := r.Header.Get("If-Match")
	if value == "" {
		replyError(w, 428, "Reload the provider pool and send its ETag in If-Match")
		return 0, false
	}
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		replyError(w, 400, "If-Match must contain one quoted revision")
		return 0, false
	}
	revision, err := strconv.ParseInt(value[1:len(value)-1], 10, 64)
	if err != nil || revision < 1 || value != `"`+strconv.FormatInt(revision, 10)+`"` {
		replyError(w, 400, "Invalid provider pool revision")
		return 0, false
	}
	return revision, true
}

func (h *ProviderPoolHandler) poolMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, providerpool.ErrNotFound):
		replyError(w, 404, "Provider pool not found")
	case errors.Is(err, providerpool.ErrStale):
		replyError(w, 412, "Provider pool changed; reload before retrying")
	case errors.Is(err, providerpool.ErrConflict):
		replyError(w, 409, "A provider pool with this name already exists")
	case errors.Is(err, providerpool.ErrCrossOwner):
		replyError(w, 400, "Pooling accounts from different owners requires explicit consent")
	case errors.Is(err, providerpool.ErrInvalid):
		replyError(w, 400, "Invalid provider pool or member")
	default:
		replyInternalError(w, h.logger, "change provider pool", err)
	}
}

func (h *ProviderPoolHandler) Update(w http.ResponseWriter, r *http.Request) {
	ws, ok := providerPoolWorkspace(w, r)
	if !ok {
		return
	}
	revision, ok := providerPoolRevision(w, r)
	if !ok {
		return
	}
	var body struct {
		Name            string                   `json:"name"`
		AllowCrossOwner bool                     `json:"allow_cross_owner"`
		Members         []providerPoolMemberView `json:"members"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		replyError(w, 400, "Invalid JSON body")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		replyError(w, 400, "Expected one JSON object")
		return
	}
	p := providerpool.Pool{ID: r.PathValue("poolId"), WorkspaceID: ws, Name: body.Name, Policy: providerpool.Policy{AllowCrossOwner: body.AllowCrossOwner}}
	for _, m := range body.Members {
		p.Members = append(p.Members, providerpool.Member{CredentialID: m.CredentialID, Priority: m.Priority})
	}
	if err := providerpool.NewStore(h.db).Update(r.Context(), p, revision); err != nil {
		h.poolMutationError(w, err)
		return
	}
	auditFromRequest(r, h.db, "provider_pool.update", "PROVIDER_POOL", p.ID, map[string]interface{}{"revision": revision + 1, "member_count": len(p.Members), "allow_cross_owner": body.AllowCrossOwner})
	// Do not reread after commit and accidentally label a later edit as ours.
	w.Header().Set("ETag", `"`+strconv.FormatInt(revision+1, 10)+`"`)
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProviderPoolHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ws, ok := providerPoolWorkspace(w, r)
	if !ok {
		return
	}
	revision, ok := providerPoolRevision(w, r)
	if !ok {
		return
	}
	id := r.PathValue("poolId")
	if err := providerpool.NewStore(h.db).Delete(r.Context(), ws, id, revision); err != nil {
		h.poolMutationError(w, err)
		return
	}
	auditFromRequest(r, h.db, "provider_pool.delete", "PROVIDER_POOL", id, map[string]interface{}{"revision": revision + 1})
	w.WriteHeader(http.StatusNoContent)
}
