package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type credentialRoutineDependent struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Resolution string `json:"resolution"` // would_resolve | another_credential | unavailable
}

type credentialDependentsResponse struct {
	Routines             []credentialRoutineDependent `json:"routines"`
	RecordedUse          string                       `json:"recorded_use"`
	VisibilityLimited    bool                         `json:"visibility_limited"`
	DynamicUsesUntracked bool                         `json:"dynamic_uses_untracked"`
}

// Dependents gives a bounded, read-only view of routine definitions that
// statically reference this credential's TYPE or its name
// (`secrets.name.<NAME>`). It never decrypts vault values.
// A type reference is not proof that a run used this credential.
func (h *CredentialHandler) Dependents(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	credID := r.PathValue("credentialId")
	role := RoleFromContext(r.Context())
	filter, filterArgs := credentialVisibilityFilter(role, UserFromContext(r.Context()))
	var credType, credName string
	args := append([]any{credID, workspaceID}, filterArgs...)
	err := h.db.QueryRowContext(r.Context(), `SELECT c.type, c.name FROM credentials c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`+filter, args...).Scan(&credType, &credName)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Credential not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load credential dependents", err)
		return
	}

	store := pipeline.NewStore(h.db)
	selectID := pipeline.NewVaultCredentialIDResolver(h.db)
	includeHidden := canRole(role, "manage")
	result := make([]credentialRoutineDependent, 0)
	selectedByCrew := make(map[string]string)
	for offset := 0; ; offset += 500 {
		routines, err := store.List(r.Context(), pipeline.ListFilters{
			WorkspaceID: workspaceID, IncludeHidden: includeHidden,
			IncludeEphemeral: false, Limit: 500, Offset: offset,
		})
		if err != nil {
			replyInternalError(w, h.logger, "list credential dependents", err)
			return
		}
		for _, routine := range routines {
			definition, err := pipeline.Parse([]byte(routine.DefinitionJSON))
			if err != nil {
				// A broken stored definition makes a negative answer unsafe.
				replyInternalError(w, h.logger, "parse credential dependent", err)
				return
			}
			for _, reference := range pipeline.ReferencedCredentialTypes(definition) {
				// A type reference may select this credential; a by-name
				// reference selects it only when it names it.
				if name, byName := pipeline.CredentialRefName(reference); byName {
					if name != credName {
						continue
					}
				} else if !strings.EqualFold(reference, credType) {
					continue
				}
				cacheKey := routine.AuthorCrewID + "\x00" + reference
				selected, cached := selectedByCrew[cacheKey]
				if !cached {
					selected, err = selectID(r.Context(), pipeline.RunScope{
						WorkspaceID: workspaceID, AuthorCrewID: routine.AuthorCrewID,
					}, reference)
					if err != nil {
						replyInternalError(w, h.logger, "resolve credential dependent", err)
						return
					}
					selectedByCrew[cacheKey] = selected
				}
				resolution := "unavailable"
				if selected == credID {
					resolution = "would_resolve"
				} else if selected != "" {
					resolution = "another_credential"
				}
				result = append(result, credentialRoutineDependent{
					Slug: routine.Slug, Name: routine.Name, Type: reference, Resolution: resolution,
				})
			}
		}
		if len(routines) < 500 {
			break
		}
	}
	writeJSON(w, http.StatusOK, credentialDependentsResponse{
		Routines:             result,
		RecordedUse:          "not_attributed",
		VisibilityLimited:    !includeHidden,
		DynamicUsesUntracked: true,
	})
}
