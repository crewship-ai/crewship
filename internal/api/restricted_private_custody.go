package api

import (
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/access"
)

type documentPrivateReadKey struct{}
type documentPrivateReadAdmission struct{ pageID string }

// Admission only: the actual handler/Store still checks exact credentials and
// current custody again before returning content. No role/grant widens custody.
var restrictedPrivateCustody = restrictedRule{check: func(m *AuthMiddleware, _ access.Store, r *http.Request, userID string) error {
	// These Page/Docs read paths carry no workspace path parameter.
	workspace := r.URL.Query().Get("workspace_id")
	if workspace == "" {
		workspace = r.Header.Get("X-Workspace-ID")
	}
	column, ref := "slug", r.PathValue("slug")
	if admission, ok := r.Context().Value(documentPrivateReadKey{}).(documentPrivateReadAdmission); ok {
		column, ref = "id", admission.pageID
	} else if strings.HasPrefix(ref, "@") {
		column, ref = "id", strings.TrimPrefix(ref, "@")
		if !immutablePageReferenceID.MatchString(ref) {
			return access.ErrDenied
		}
	}
	var id string
	return m.db.QueryRowContext(r.Context(), `SELECT p.id FROM pages p
 JOIN workspaces w ON w.id=p.workspace_id AND w.deleted_at IS NULL
 JOIN workspace_members member ON member.workspace_id=w.id AND member.user_id=?
 JOIN users u ON u.id=member.user_id AND u.suspended_at IS NULL
 WHERE (w.id=? OR w.slug=?) AND p.`+column+`=? AND p.private_audience=1 AND p.owner_user_id=u.id`, userID, workspace, workspace, ref).Scan(&id)
}}
