package backup

import "fmt"

// IsAdminRole reports whether the given workspace membership role is
// permitted to create or restore backups. The contract matches the
// existing `canRole(role, "manage")` helper in internal/api: only
// OWNER and ADMIN qualify. MANAGER, MEMBER and VIEWER are refused.
//
// Instance-scope operations use an additional server-level OWNER check
// in PR 4 (CRE-129); this helper covers only workspace/crew scope.
func IsAdminRole(role string) bool {
	return role == "OWNER" || role == "ADMIN"
}

// RequireAdmin returns an error suitable for HTTP 403 / CLI exit if
// role is not an admin role. The error wraps ErrAdminRequired so
// callers can identify it via errors.Is rather than substring match.
func RequireAdmin(role string) error {
	if !IsAdminRole(role) {
		return fmt.Errorf("%w (have %q); only OWNER and ADMIN can create or restore backups", ErrAdminRequired, role)
	}
	return nil
}
