package api

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// The workspace default look (WSD-01): what a member sees until they choose
// their own. Shape and size are checked here; the style, accent and knob ids
// are not enumerated, so a new style needs no backend release — a client that
// does not know an id paints the built-in look instead.
var (
	appearanceDefaultID     = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	appearanceDefaultThemes = map[string]bool{"dark": true, "light": true, "system": true}
)

const (
	appearanceDefaultMaxBytes    = 2048
	appearanceDefaultMaxKnobs    = 16
	appearanceDefaultMaxKnobText = 64
)

// validateWorkspaceAppearanceDefault accepts a JSON object with the keys
// style, knobs, accent and theme, each optional; `{}` resets the default.
func validateWorkspaceAppearanceDefault(raw json.RawMessage) error {
	if len(raw) > appearanceDefaultMaxBytes {
		return fmt.Errorf("appearance_default exceeds 2 KiB")
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("appearance_default must be an object with style, knobs, accent and theme")
	}
	for key, value := range values {
		switch key {
		case "style", "accent":
			var id string
			if err := json.Unmarshal(value, &id); err != nil || !appearanceDefaultID.MatchString(id) {
				return fmt.Errorf("appearance_default.%s must be an id of lowercase letters, digits and dashes (up to 32)", key)
			}
		case "theme":
			var theme string
			if err := json.Unmarshal(value, &theme); err != nil || !appearanceDefaultThemes[theme] {
				return fmt.Errorf("appearance_default.theme must be dark, light or system")
			}
		case "knobs":
			var knobs map[string]string
			if err := json.Unmarshal(value, &knobs); err != nil || knobs == nil {
				return fmt.Errorf("appearance_default.knobs must be an object of text values")
			}
			if len(knobs) > appearanceDefaultMaxKnobs {
				return fmt.Errorf("appearance_default.knobs holds at most %d entries", appearanceDefaultMaxKnobs)
			}
			for name, v := range knobs {
				if !appearanceDefaultID.MatchString(name) || len(v) > appearanceDefaultMaxKnobText {
					return fmt.Errorf("appearance_default.knobs needs id names and values up to %d characters", appearanceDefaultMaxKnobText)
				}
			}
		default:
			return fmt.Errorf("appearance_default supports style, knobs, accent and theme")
		}
	}
	return nil
}
