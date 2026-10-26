package api

import (
	"database/sql"
	"net/http"

	"github.com/crewship-ai/crewship/internal/access"
)

type sharedCrewAgent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Role   string `json:"agent_role"`
	Status string `json:"status"`
}
type sharedCrewResponse struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Slug       string            `json:"slug"`
	Restricted bool              `json:"restricted"`
	AgentCount int               `json:"agent_count"`
	Agents     []sharedCrewAgent `json:"agents,omitempty"`
}
type crewAccessMeResponse struct {
	CrewID  string                      `json:"crew_id"`
	Actions map[string]accessMeDecision `json:"actions"`
}

// The live root grant validates recipient, issuer, workspace and crew together.
// Use also permits View, without disclosing any configuration or activity.
const sharedCrewFrom = ` FROM crews c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id AND wm.user_id=?
 WHERE c.workspace_id=? AND c.deleted_at IS NULL AND c.kind<>'setup'
 AND EXISTS(SELECT 1 FROM live_access_grants g WHERE g.member_id=wm.id AND g.resource_kind='crew' AND g.crew_id=c.id AND g.operation IN ('view','use'))`

func (h *CrewHandler) restrictedCrewDirectory(w http.ResponseWriter, r *http.Request, accessOnly bool) bool {
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	restricted, err := (access.Store{DB: h.db}).HasRestrictedMembership(r.Context(), user.ID)
	if err != nil {
		replyError(w, 500, "Crew unavailable")
		return true
	}
	if !restricted {
		return false
	}
	workspace, id := WorkspaceIDFromContext(r.Context()), r.PathValue("crewId")
	// Search identities only. Descriptions/configuration must not become an oracle.
	searchSQL, searchArgs := listSearchClause(r, "c.name", "c.slug")
	predicate := sharedCrewFrom + ` AND (?='' OR c.id=?)` + searchSQL
	args := append([]any{user.ID, workspace, id, id}, searchArgs...)
	limit, offset := parsePagination(r, 100, 500)
	if id != "" {
		limit, offset = 1, 0
	}
	total := 0
	if id == "" {
		if err = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*)`+predicate, args...).Scan(&total); err != nil {
			replyError(w, 500, "Crew unavailable")
			return true
		}
	}
	order := "c.created_at DESC,c.id DESC"
	if r.URL.Query().Get("order") == "name" {
		order = "c.name COLLATE NOCASE,c.id"
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT c.id,c.name,c.slug,
 (SELECT COUNT(*) FROM agents a WHERE a.crew_id=c.id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL),
 EXISTS(SELECT 1 FROM live_access_grants g WHERE g.member_id=wm.id AND g.resource_kind='crew' AND g.crew_id=c.id AND g.operation='use')`+predicate+` ORDER BY `+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		replyError(w, 500, "Crew unavailable")
		return true
	}
	result := []sharedCrewResponse{}
	canUse := false
	for rows.Next() {
		item := sharedCrewResponse{Restricted: true}
		if err = rows.Scan(&item.ID, &item.Name, &item.Slug, &item.AgentCount, &canUse); err != nil {
			break
		}
		result = append(result, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		replyError(w, 500, "Crew unavailable")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Crew-Access-Mode", "restricted")
	if id == "" {
		writeListMeta(w, total, limit, offset)
		writeJSON(w, 200, result)
		return true
	}
	if len(result) != 1 {
		replyError(w, 404, "Crew not found")
		return true
	}
	if accessOnly {
		use := accessDecision("denied", "missing_crew_use_share")
		if canUse {
			use = accessDecision("conditional", "agent_authority_and_runtime_preflight_required")
		}
		writeJSON(w, 200, crewAccessMeResponse{CrewID: id, Actions: map[string]accessMeDecision{
			"view": accessDecision("allowed", "explicit_crew_share"), "use": use,
			"manage": accessDecision("denied", "guest_access_ceiling"),
		}})
		return true
	}
	// Couple new agent data to the live root in this same SQLite snapshot.
	// LEFT JOIN preserves a visible empty crew; zero roots means authority loss.
	agents, err := h.db.QueryContext(r.Context(), `SELECT visible.id,a.id,a.name,a.slug,a.agent_role,a.status FROM (SELECT c.id`+sharedCrewFrom+` AND c.id=?) visible LEFT JOIN agents a ON a.crew_id=visible.id AND a.workspace_id=? AND a.deleted_at IS NULL ORDER BY a.slug,a.id`, user.ID, workspace, id, workspace)
	if err != nil {
		replyError(w, 500, "Crew unavailable")
		return true
	}
	defer agents.Close()
	rootFound := false
	for agents.Next() {
		var root string
		var aid, name, slug, role, status sql.NullString
		if err = agents.Scan(&root, &aid, &name, &slug, &role, &status); err != nil {
			break
		}
		rootFound = true
		if aid.Valid {
			result[0].Agents = append(result[0].Agents, sharedCrewAgent{ID: aid.String, Name: name.String, Slug: slug.String, Role: role.String, Status: status.String})
		}
	}
	if err == nil {
		err = agents.Err()
	}
	if err != nil {
		replyError(w, 500, "Crew unavailable")
		return true
	}
	if !rootFound {
		replyError(w, http.StatusNotFound, "Crew not found")
		return true
	}
	result[0].AgentCount = len(result[0].Agents)
	writeJSON(w, http.StatusOK, result[0])
	return true
}

// MyCrewAccess explains only the current actor's explicit Guest crew share.
// Trusted members continue to use their existing per-operation role gates.
func (h *CrewHandler) MyCrewAccess(w http.ResponseWriter, r *http.Request) {
	if !h.restrictedCrewDirectory(w, r, true) {
		replyError(w, http.StatusNotFound, "Crew access unavailable")
	}
}
