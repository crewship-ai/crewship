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
// statically reference this credential's TYPE. It never decrypts vault values.
// A type reference is not proof that a run used this credential.
func (h *CredentialHandler) Dependents(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	credID := r.PathValue("credentialId")
	role := RoleFromContext(r.Context())
	filter, filterArgs := credentialVisibilityFilter(role, UserFromContext(r.Context()))
	var credType string
	args := append([]any{credID, workspaceID}, filterArgs...)
	err := h.db.QueryRowContext(r.Context(), `SELECT c.type FROM credentials c
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`+filter, args...).Scan(&credType)
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
				if !strings.EqualFold(reference, credType) {
					continue
				}
				selected, cached := selectedByCrew[routine.AuthorCrewID]
				if !cached {
					selected, err = selectID(r.Context(), pipeline.RunScope{
						WorkspaceID: workspaceID, AuthorCrewID: routine.AuthorCrewID,
					}, reference)
					if err != nil {
						replyInternalError(w, h.logger, "resolve credential dependent", err)
						return
					}
					selectedByCrew[routine.AuthorCrewID] = selected
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
