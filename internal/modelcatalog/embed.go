package modelcatalog

import (
	_ "embed"
	"sync"
)

// snapshotJSON is a TRIMMED models.dev snapshot, not the full index.
//
//	Source:  https://models.dev/api.json
//	Fetched: 2026-08-19
//	Trim:    8 of 192 providers kept — amazon-bedrock, anthropic, deepseek,
//	         google, mistral, openai, openrouter, xai. Every model those
//	         providers publish is kept verbatim, including fields this package
//	         does not decode, so a refresh is a pure re-fetch with no editing.
//
// Why trimmed. The full index is ~4 MB and 92% of it is gateways we have no
// codec for. The kept set covers every provider that appears in
// paymaster.priceTable or llm.curatedModels, plus openrouter and
// amazon-bedrock, which are the two gateways a Phase-2 provider would reach
// through the codecs that already exist.
//
// Refresh procedure — `go generate ./internal/modelcatalog/...`, or by hand:
//
//	curl -fsS https://models.dev/api.json | jq -S 'with_entries(select(.key |
//	  IN("amazon-bedrock","anthropic","deepseek","google","mistral","openai",
//	     "openrouter","xai")))' > internal/modelcatalog/data/models.dev.json
//
// `jq -S` sorts keys so a refresh produces a reviewable diff rather than a
// reshuffle. After refreshing, run the package tests: embed_test.go pins the
// ids and the one rate that the hand-written price table also carries, and it
// is the thing that catches an upstream repricing or a renamed id.
//
// Adding a provider to the trim means editing BOTH the go:generate line and
// the by-hand command above; they are duplicated on purpose, because the
// comment is what a reader without `go generate` follows.
//
// Note on what the snapshot does NOT cover: several priceTable keys have no
// models.dev equivalent (xai/grok-4.20, xai/grok-4.1-fast,
// mistral/codestral-2508, google/gemini-1.5-*, google/gemini-2.0-flash, and
// the openai/gpt-5* aliases). That is expected and harmless — the catalog is
// a gap-filler that sits *below* the hand-verified table in the lookup order,
// never above it.
//
//go:generate sh -c "curl -fsS https://models.dev/api.json | jq -S 'with_entries(select(.key | IN(\"amazon-bedrock\",\"anthropic\",\"deepseek\",\"google\",\"mistral\",\"openai\",\"openrouter\",\"xai\")))' > data/models.dev.json"
//go:embed data/models.dev.json
var snapshotJSON []byte

// defaultCatalog decodes the snapshot once, on first use. Decoding ~650 KB of
// JSON at init would tax every binary that links this package transitively —
// including the CLI, where the catalog is never consulted.
var defaultCatalog = sync.OnceValues(func() (Catalog, error) {
	c, err := Parse(snapshotJSON)
	if err != nil {
		return Catalog{}, err
	}
	return c, nil
})

// Default returns the embedded snapshot. It is never nil and it never panics:
// a corrupt snapshot degrades to "no catalog data", which makes every caller
// fall through to its own fallback (curated model lists, the provider price
// ceiling) instead of taking the server down at startup over a data file.
//
// The returned Catalog is shared. Its maps must be treated as read-only —
// Lookup and Models hand out copies precisely so callers never need to reach
// into it directly.
func Default() Catalog {
	c, _ := defaultCatalog()
	return c
}

// DefaultErr reports why Default came back empty, or nil when the snapshot
// decoded cleanly. It exists so the test suite can assert the committed file
// is valid — the production paths deliberately have nowhere to report this
// to, since a leaf package with no logger dependency is the point.
func DefaultErr() error {
	_, err := defaultCatalog()
	return err
}
