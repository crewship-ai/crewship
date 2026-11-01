package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/featureflags"
)

// CraftVersionHistoryFlag is the workspace feature flag for native Word and
// Sheet version history. An undefined flag is off.
const CraftVersionHistoryFlag = "craft_version_history"

// craftNativeVersionsLimit bounds one listing in SQL; older versions stay
// stored and restorable, the response only says it was truncated.
const craftNativeVersionsLimit = 200

type craftNativeVersionAuthorResponse struct {
	ActorID     string `json:"actorId"`
	Kind        string `json:"kind"`
	DisplayName string `json:"displayName"`
}
type craftNativeVersionResponse struct {
	Version      string                           `json:"version"`
	CreatedAt    string                           `json:"createdAt"`
	Author       craftNativeVersionAuthorResponse `json:"author"`
	RestoredFrom string                           `json:"restoredFrom,omitempty"`
	Current      bool                             `json:"current"`
}
type craftNativeVersionsResponse struct {
	CurrentVersion string                       `json:"currentVersion"`
	Versions       []craftNativeVersionResponse `json:"versions"`
	Truncated      bool                         `json:"truncated"`
}

// listVersions is read-only. It binds the same private native admission and
// live Page Read authority as the whole document read, so a person gets exactly
// the refusal that read returns; only then does the workspace flag apply.
// Restoring stays the fenced POST document/restore; there is no second write path.
func (d *craftNativeDocumentsHTTP) listVersions(w http.ResponseWriter, r *http.Request) {
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	enabled, err := featureflags.IsEnabled(r.Context(), d.human.pages.db, entry.scope.WorkspaceID, CraftVersionHistoryFlag)
	if err != nil {
		replyError(w, 500, "native version history unavailable")
		return
	}
	if !enabled {
		replyError(w, 404, "native version history is not enabled")
		return
	}
	versions, truncated, err := s.Versions(r.Context(), entry.scope, craftNativeVersionsLimit)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	out := craftNativeVersionsResponse{Versions: make([]craftNativeVersionResponse, 0, len(versions)), Truncated: truncated}
	names := map[string]craftNativeVersionAuthorResponse{}
	for i, v := range versions {
		author, ok := names[v.Actor]
		if !ok {
			author = d.versionAuthor(r, entry.scope.WorkspaceID, v.Actor)
			names[v.Actor] = author
		}
		item := craftNativeVersionResponse{Version: strconv.FormatInt(v.Version, 10), CreatedAt: v.CreatedAt, Author: author, Current: i == 0}
		if v.RestoredFrom > 0 {
			item.RestoredFrom = strconv.FormatInt(v.RestoredFrom, 10)
		}
		if i == 0 {
			out.CurrentVersion = item.Version
		}
		out.Versions = append(out.Versions, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

// versionAuthor names a recorded actor the way the lease bar names a holder.
// Agents resolve only within this workspace.
func (d *craftNativeDocumentsHTTP) versionAuthor(r *http.Request, workspaceID, actor string) craftNativeVersionAuthorResponse {
	db := d.human.pages.db
	switch {
	case strings.HasPrefix(actor, "agent:"):
		name := "Agent"
		_ = db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(name,''),'Agent') FROM agents WHERE id=? AND workspace_id=?`, strings.TrimPrefix(actor, "agent:"), workspaceID).Scan(&name)
		return craftNativeVersionAuthorResponse{ActorID: actor, Kind: "agent", DisplayName: name}
	case strings.HasPrefix(actor, "user:"):
		name := "User"
		_ = db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(full_name,''),'User') FROM users WHERE id=?`, strings.TrimPrefix(actor, "user:")).Scan(&name)
		return craftNativeVersionAuthorResponse{ActorID: actor, Kind: "human", DisplayName: name}
	default:
		return craftNativeVersionAuthorResponse{ActorID: actor, Kind: "system", DisplayName: "Crewship"}
	}
}
