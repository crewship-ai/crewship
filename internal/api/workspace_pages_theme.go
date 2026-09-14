package api

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var pageThemeColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Appearance only: no arbitrary CSS, URLs, credentials or execution settings.
// An empty object resets to the SDK defaults; omitted keys inherit defaults.
func validateWorkspacePagesTheme(raw json.RawMessage) error {
	if len(raw) > 1024 {
		return fmt.Errorf("pages_theme exceeds 1 KiB")
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return fmt.Errorf("pages_theme must be a color object")
	}
	allowed := map[string]bool{"accent": true, "background": true, "surface": true, "text": true, "muted": true, "border": true}
	for key, value := range values {
		if !allowed[key] || !pageThemeColor.MatchString(value) {
			return fmt.Errorf("pages_theme supports accent, background, surface, text, muted and border as #RRGGBB colors")
		}
	}
	return nil
}
