package llm

import "strings"

// Curated model fallback — the single source of truth for "what models exist"
// when a provider can't be reached live (no credential, network down, or the
// provider doesn't implement ModelLister). Keyed by provider identifier; the
// lookup is case-insensitive so both the API enum form ("ANTHROPIC") and the
// lowercase Provider.Name() form ("anthropic") resolve.
//
// The Claude (ANTHROPIC) ids here are the current generally-available model
// strings — bare aliases, no date suffixes (date-suffixed aliases 404 against
// the Messages API). Keep this list ordered most-capable-first so a UI that
// renders it top-to-bottom presents the recommended default first.
var curatedModels = map[string][]ModelInfo{
	"anthropic": {
		{ID: "claude-opus-4-8", DisplayName: "Claude Opus 4.8", Provider: "anthropic"},
		{ID: "claude-opus-4-7", DisplayName: "Claude Opus 4.7", Provider: "anthropic"},
		{ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6", Provider: "anthropic"},
		{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", Provider: "anthropic"},
		{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", Provider: "anthropic"},
	},
	"openai": {
		{ID: "gpt-4o", DisplayName: "GPT-4o", Provider: "openai"},
		{ID: "gpt-4o-mini", DisplayName: "GPT-4o mini", Provider: "openai"},
		{ID: "o3", DisplayName: "o3", Provider: "openai"},
		{ID: "o3-mini", DisplayName: "o3-mini", Provider: "openai"},
	},
	"google": {
		{ID: "gemini-2.0-flash", DisplayName: "Gemini 2.0 Flash", Provider: "google"},
		{ID: "gemini-1.5-pro", DisplayName: "Gemini 1.5 Pro", Provider: "google"},
		{ID: "gemini-1.5-flash", DisplayName: "Gemini 1.5 Flash", Provider: "google"},
	},
}

// CuratedModels returns the fallback model set for a provider, or nil when the
// provider has no curated list (e.g. OLLAMA, whose model set is purely local
// and must be discovered live via /api/tags — there is no sensible static
// list). The returned slice is a copy so callers can sort/append freely.
func CuratedModels(provider string) []ModelInfo {
	src, ok := curatedModels[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return nil
	}
	out := make([]ModelInfo, len(src))
	copy(out, src)
	return out
}
