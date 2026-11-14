package api

import (
	"errors"
	"fmt"
)

// maxLLMModelIDLen bounds a hand-typed model id. The longest real ids are
// gateway routes such as "openrouter/anthropic/claude-sonnet-5.5" or cloud
// deployment names; 200 leaves room for those without accepting a pasted
// paragraph.
const maxLLMModelIDLen = 200

// validateLLMModelID checks that a model id is well formed. It deliberately
// does not check the id against any catalog: providers ship models faster
// than Crewship's catalog is refreshed, accounts see models the catalog does
// not list, and an operator must be able to pin an exact id by hand (#5376).
//
// The accepted alphabet covers every id form the runners take: provider
// slugs (claude-sonnet-5-5, gpt-6.1-sol), gateway routes with "/", Ollama
// tags with ":", Bedrock and Vertex forms with "." "@" and ":", and Claude
// Code's bracketed context suffix ("claude-opus-5-5[1m]"). Whitespace,
// quotes and shell metacharacters are refused so an id can never be read
// as more than one argument by a runner wrapper. An empty id is allowed; it
// means "use the runner's default".
func validateLLMModelID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > maxLLMModelIDLen {
		return fmt.Errorf("llm_model must be at most %d characters", maxLLMModelIDLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.', r == ':', r == '/', r == '@', r == '+', r == '[', r == ']':
		default:
			return errors.New("llm_model may contain only letters, digits and - _ . : / @ + [ ]")
		}
	}
	return nil
}
