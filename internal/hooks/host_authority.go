package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// HostAuthorized checks approval of the exact stored revision. The digest is
// provenance, not a secret or a signature: API callers cannot write the column,
// and backup import must strip it rather than importing execution authority.
func HostAuthorized(h Hook) bool {
	return h.HostAuthorization != "" && h.HostAuthorization == hostAuthorization(h)
}

func hostAuthorization(h Hook) string {
	cfg := h.HandlerConfig
	if cfg == nil {
		cfg = map[string]any{}
	}
	b, err := json.Marshal(struct {
		ID, Workspace, Crew, Event, Kind string
		Matcher                          Matcher
		Config                           map[string]any
		Blocking, Enabled                bool
		Updated                          string
	}{h.ID, h.WorkspaceID, h.CrewID, string(h.Event), string(h.HandlerKind), h.Matcher, cfg, h.Blocking, h.Enabled, tsformat.Format(h.UpdatedAt)})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:])
}
