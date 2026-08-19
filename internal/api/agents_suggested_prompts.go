package api

// Per-agent chat suggestions — the normaliser for agents.suggested_prompts
// (PRD chat-as-a-primary-surface, Step 7).
//
// One prompt per line. The column is edited through a textarea, so this is
// the one place that turns what a person typed into what is stored: CRLF
// folded to LF, each line trimmed, blank lines dropped, and the two caps
// enforced. Everything downstream — the API, the chat panel, the CLI — reads
// a canonical value and never has to guess.
//
// Errors name the offending prompt by position, because "invalid input" on a
// list of eight is a hunt.

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// maxSuggestedPrompts caps the list. Eight is what fits under a composer
	// without the chips becoming their own screen; the client mirrors it in
	// lib/agent-suggestions.ts (MAX_SUGGESTED_PROMPTS).
	maxSuggestedPrompts = 8
	// maxSuggestedPromptLength caps one prompt, in CHARACTERS not bytes — a
	// byte cap would silently give a Czech or Japanese author a shorter field
	// than an English one.
	maxSuggestedPromptLength = 120
)

// normalizeSuggestedPrompts validates and canonicalises the raw textarea
// contents. It returns the value to store — LF-separated, trimmed, blank
// lines removed — or an error naming exactly what is wrong.
//
// An input that is empty or contains only whitespace normalises to "", which
// callers store as NULL: "not configured", which falls back to the role packs.
func normalizeSuggestedPrompts(raw string) (string, error) {
	// Fold both foreign line endings before splitting. A textarea posted from
	// a browser on Windows sends CRLF, and a stray CR would otherwise ride
	// along inside the stored prompt and render as a broken chip.
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	prompts := make([]string, 0, maxSuggestedPrompts)
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		// A blank line is how people separate things while typing; it is not
		// a prompt, so it neither counts towards the cap nor is stored.
		if line == "" {
			continue
		}
		// Position is 1-based over the prompts a reader can SEE (blank lines
		// skipped), which is what "prompt 3" means to the person editing.
		if n := utf8.RuneCountInString(line); n > maxSuggestedPromptLength {
			return "", fmt.Errorf("prompt %d exceeds %d characters (it has %d)",
				len(prompts)+1, maxSuggestedPromptLength, n)
		}
		prompts = append(prompts, line)
	}

	// The count is checked after the loop rather than inside it so a list that
	// is both too long and has an over-long entry reports the entry — the
	// specific, fixable complaint — rather than only the count.
	if len(prompts) > maxSuggestedPrompts {
		return "", fmt.Errorf("at most %d prompts are allowed (got %d)",
			maxSuggestedPrompts, len(prompts))
	}

	return strings.Join(prompts, "\n"), nil
}

// suggestedPromptsPatch resolves the value a PATCH body wants written.
//
// Returns (value, ok, err): ok=false means the key was absent and the column
// must be left alone. A nil value means "store NULL" — both an explicit JSON
// null and a textarea the user emptied land there, so the column has one
// representation of "not configured".
func suggestedPromptsPatch(v interface{}) (interface{}, bool, error) {
	if v == nil {
		return nil, true, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return nil, false, fmt.Errorf("suggested_prompts must be a string with one prompt per line")
	}
	normalized, err := normalizeSuggestedPrompts(s)
	if err != nil {
		return nil, false, err
	}
	if normalized == "" {
		return nil, true, nil
	}
	return normalized, true, nil
}
