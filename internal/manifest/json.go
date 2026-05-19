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
	// json.Encoder.Encode always appends exactly one newline. Use
	// TrimSuffix instead of TrimRight so we strip only that single
	// newline — TrimRight would over-trim a payload whose own
	// trailing content happens to be newline-suffixed (e.g. a JSON
	// string value ending in `"\n"`).
	out := strings.TrimSuffix(sb.String(), "\n")
	return []byte(out), nil
}
