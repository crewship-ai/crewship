package api

// Per-agent ask forms — the PATCH-side adapter for agents.ask_forms.
//
// Thin on purpose. Everything that decides whether a definition is allowed
// near a user lives in internal/askforms, because the CLI, the seed path and
// any future importer must apply the same rules, and a validator that only
// the HTTP handler runs is a validator with a way around it.
//
// What is left here is the one thing that IS HTTP: turning "the key was in
// the body" / "the key was absent" / "the key was null" into what the update
// builder needs. Same three-way answer suggestedPromptsPatch gives, and for
// the same reason — an emptied editor and an explicit null must land on one
// stored representation of "not configured".

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/askforms"
)

// askFormsPatch resolves the value a PATCH body wants written to ask_forms.
//
// Returns (value, ok, err): ok=false means the key was absent and the column
// must be left alone. A nil value means "store NULL" — an explicit JSON null,
// an emptied editor and an empty array all land there.
//
// The accepted wire type is a STRING holding the JSON document, not a nested
// JSON array. That looks odd until you see where it comes from: the column is
// TEXT, the config tab edits it in a textarea, and `--ask-forms @forms.json`
// reads a file. Accepting a nested array as well would mean two encodings of
// the same value on one endpoint, and the first caller to send the wrong one
// would get a confusing error instead of a definition.
func askFormsPatch(v interface{}) (interface{}, bool, error) {
	if v == nil {
		return nil, true, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return nil, false, fmt.Errorf(
			"ask_forms must be a string holding a JSON array of form definitions")
	}
	normalized, err := askforms.Normalize(s)
	if err != nil {
		return nil, false, err
	}
	if normalized == "" {
		return nil, true, nil
	}
	return normalized, true, nil
}
