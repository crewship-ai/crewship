package pipeline

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Bounds on the slash palette block. Both exist because the values are
// rendered into a palette row and a repl banner — surfaces with a fixed
// width and no truncation of their own — not because storage cares.
const (
	// MaxSlashLabelLen is the cap on `slash.label` / `slash.label_cs`, in
	// runes rather than bytes so a Czech label is not silently shorter
	// than an English one.
	MaxSlashLabelLen = 80
	// MaxSlashIconLen bounds the lucide icon name. Icon names are short
	// kebab-case identifiers; anything longer is a mistake, and the wire
	// shape is not a place to smuggle a payload.
	MaxSlashIconLen = 40
)

// validateSlash checks the optional `slash` block.
//
// The rules are deliberately shallow. `enabled` is the only field with
// semantics, and the two strings are presentation: the icon name is an
// OPEN set (the dashboard resolves what it knows and falls back for what
// it doesn't — see SlashSpec.Icon), so validating it against an
// enumeration here would make every new icon a coordinated release
// across two repos. What is checked is the shape: a bounded length, and
// an icon that looks like an identifier rather than markup or a URL.
//
// A block with `enabled:false` is still validated. A routine that is
// staged to join the palette later should fail at save time, when the
// author is looking at it, rather than at the moment someone flips the
// switch.
func validateSlash(dsl *DSL) error {
	s := dsl.Slash
	if s == nil {
		return nil
	}
	if n := utf8.RuneCountInString(s.Label); n > MaxSlashLabelLen {
		return fmt.Errorf("pipeline: slash.label is %d characters, over the %d limit", n, MaxSlashLabelLen)
	}
	if n := utf8.RuneCountInString(s.LabelCS); n > MaxSlashLabelLen {
		return fmt.Errorf("pipeline: slash.label_cs is %d characters, over the %d limit", n, MaxSlashLabelLen)
	}
	if s.Label != strings.TrimSpace(s.Label) || s.LabelCS != strings.TrimSpace(s.LabelCS) {
		return fmt.Errorf("pipeline: slash label must not have leading or trailing whitespace")
	}
	if strings.ContainsAny(s.Label, "\r\n") || strings.ContainsAny(s.LabelCS, "\r\n") {
		return fmt.Errorf("pipeline: slash label must be a single line")
	}
	if err := validateSlashIcon(s.Icon); err != nil {
		return err
	}
	return nil
}

// validateSlashIcon bounds the icon name to the shape every lucide name
// has: lowercase letters, digits and dashes. An empty icon is fine (the
// dashboard derives one), so only a non-empty malformed name is an error.
func validateSlashIcon(icon string) error {
	if icon == "" {
		return nil
	}
	if len(icon) > MaxSlashIconLen {
		return fmt.Errorf("pipeline: slash.icon is %d characters, over the %d limit", len(icon), MaxSlashIconLen)
	}
	for _, r := range icon {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return fmt.Errorf("pipeline: slash.icon %q must be a lowercase kebab-case lucide icon name (a-z 0-9 -)", icon)
		}
	}
	return nil
}
