package manifest

import (
	"encoding/json"
	"strings"
)

// jsonMarshal is a thin wrapper around encoding/json.Marshal with
// HTML-escape turned off. devcontainer.json strings end up embedded
// in YAML and may include characters like `<` and `&` that the
// default Marshal would escape to <, breaking string equality
// for users who hand-author the same config — round-tripping a
// manifest must be byte-identical to ensure idempotent reapply.
func jsonMarshal(v any) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := strings.TrimRight(sb.String(), "\n")
	return []byte(out), nil
}
