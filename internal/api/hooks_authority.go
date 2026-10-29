package api

import "net/http"

func (h *HooksHandler) hostHookAuthority(r *http.Request) bool {
	return canScope(r.Context(), scopeInstanceAdmin) && isInstanceAdmin(r, h.db)
}

func (h *HooksHandler) authorizeHookWrite(w http.ResponseWriter, r *http.Request, host bool) bool {
	if host {
		if !h.hostHookAuthority(r) {
			replyError(w, http.StatusForbidden, "host shell hooks require an instance administrator and instance:admin token scope; ask an instance administrator to review and re-enable the hook")
			return false
		}
	} else if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "managing hooks requires workspace OWNER or ADMIN role")
		return false
	}
	return true
}
