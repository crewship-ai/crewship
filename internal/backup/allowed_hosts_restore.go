package backup

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/credhosts"
)

const allowedHostsColumn = "allowed_hosts"

// normalizeRestoredAllowedHosts validates a bundle's credentials.allowed_hosts
// before it is bound to the insert (#5045). The column is a delivery
// precondition a person approved, so a bundle must not be able to land
// anything but a clean list:
//   - NULL, empty and "[]" stay as they are (the credential is unbound);
//   - a readable list is rewritten in canonical form (lower-case, no trailing
//     dot, de-duplicated), the same form the API stores;
//   - anything else (not JSON, wrong type, an entry that is not a valid host)
//     is kept as an unreadable string. Delivery treats an unreadable list on a
//     host-bound credential as "withhold", so a damaged value can never widen
//     delivery, and the operator sees it and sets it again.
func normalizeRestoredAllowedHosts(v any) any {
	var raw string
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		raw = t
	case []byte:
		raw = string(t)
	default:
		return fmt.Sprintf("%v", v)
	}
	if s := strings.TrimSpace(raw); s == "" || s == "[]" {
		return v
	}
	var hosts []string
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		return raw
	}
	norm, err := credhosts.NormalizeList(hosts)
	if err != nil {
		return raw
	}
	if len(norm) == 0 {
		return "[]"
	}
	enc, err := json.Marshal(norm)
	if err != nil {
		return raw
	}
	return string(enc)
}
