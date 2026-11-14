package devcontainer

import (
	"encoding/json"
	"strings"
)

// NormalizeAgentUserJSON applies NormalizeAgentUser to a devcontainer.json
// document and returns the form Crewship stores: crew create and crew
// update run it on every config they save, and both apply planners run it
// on the config a manifest declares, so the two compare equal and a
// re-apply plans no update (#5337).
//
// Only the features block is rewritten. Every other top-level key is kept as
// the author wrote it, including keys the Config model does not carry
// (hostRequirements, customizations), so a manifest that declares them still
// matches the stored config. A document that needs no change is returned
// byte for byte. A config that parses and has a conflicting account returns
// the ErrAgentAccountConflict error.
func NormalizeAgentUserJSON(raw string) (string, error) {
	cfg, err := ParseBytes([]byte(raw))
	if err != nil {
		return raw, err
	}
	changed, err := cfg.NormalizeAgentUser()
	if err != nil {
		return raw, err
	}
	if !changed {
		return raw, nil
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return raw, err
	}
	doc["features"] = cfg.Features
	// No HTML escaping: the manifest packages compare and store devcontainer
	// strings without it, and a config holding `&` or `<` must come out
	// byte-identical on the CLI and on the server.
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return raw, err
	}
	return strings.TrimSuffix(sb.String(), "\n"), nil
}
