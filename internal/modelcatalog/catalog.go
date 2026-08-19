// Package modelcatalog decodes a models.dev catalog snapshot — the provider /
// model / capability / price index published at https://models.dev/api.json —
// into Go types, and answers offline questions about it.
//
// Why this is its own package. Two consumers need the same types: internal/llm
// (which model ids a provider offers, what they can do) and internal/paymaster
// (what a token costs). internal/llm already imports internal/paymaster
// (middleware.go), so a type both must see cannot live in either without
// creating an import cycle. This package therefore imports nothing from
// internal/ — it is a leaf by construction, and it must stay one.
//
// It also performs no I/O beyond decoding bytes a caller hands it. Default()
// reads an embedded snapshot; nothing here dials the network. That is
// deliberate: the catalog backs the *fallback* path taken when a provider
// cannot be reached live, and a fallback that needs a network call to answer
// inverts its own purpose.
package modelcatalog

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Catalog is a models.dev snapshot keyed by canonical provider id. Keys are
// lowercase and trimmed; see Parse for the normalization contract.
type Catalog map[string]Provider

// Provider is one vendor or gateway in the catalog. Env lists the environment
// variables models.dev documents as credential sources — informational here,
// since the authoritative key name for our own providers lives on
// llm.ProviderSpec.KeyEnv.
type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Env    []string         `json:"env"`
	Doc    string           `json:"doc"`
	Models map[string]Model `json:"models"`
}

// Model is one model entry. The field set is deliberately a subset of what
// models.dev publishes: unknown keys (description, reasoning_options,
// open_weights, last_updated, cost.tiers, …) decode away silently, so a
// snapshot refresh that adds upstream fields never breaks the build.
type Model struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Family           string     `json:"family"`
	Attachment       bool       `json:"attachment"`
	Reasoning        bool       `json:"reasoning"`
	ToolCall         bool       `json:"tool_call"`
	StructuredOutput bool       `json:"structured_output"`
	Temperature      bool       `json:"temperature"`
	Knowledge        string     `json:"knowledge"`
	ReleaseDate      string     `json:"release_date"`
	Modalities       Modalities `json:"modalities"`
	Limit            Limit      `json:"limit"`
	Cost             *Cost      `json:"cost,omitempty"`
}

// Modalities records what the model accepts and emits ("text", "image",
// "pdf", "audio", "video").
type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Limit is the context window and the maximum output length, in tokens. int64
// because a million-token context is already in range of int32 problems on
// 32-bit builds and these values are multiplied downstream.
type Limit struct {
	Context int64 `json:"context"`
	Output  int64 `json:"output"`
}

// Cost is the rate card for one model in USD per 1,000,000 tokens — the same
// unit paymaster's modelPrice uses, with no scaling on either side. Verified
// against the hand-written table: models.dev claude-opus-4-7 is
// {input:5, output:25, cache_read:0.5, cache_write:6.25}, and
// priceTable["anthropic/claude-opus-4-7"] is {5.00, 25.00, 0.50, 6.25}.
//
// CacheRead and CacheWrite are pointers because "absent" and "zero" are
// different facts: absent means the provider does not price that channel
// separately (so it bills at the input rate), while zero would mean free.
// Collapsing them under-bills. See Rates for the resolution.
type Cost struct {
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

// Parse decodes a models.dev api.json payload and normalizes it.
//
// Normalization happens exactly once, here, rather than on every lookup:
//
//   - Provider keys and model keys are lowercased and trimmed.
//   - Provider.ID and Model.ID are set from their (normalized) map key. When
//     the key and the embedded id disagree, the KEY wins — callers index by
//     key, so an id that does not round-trip through Lookup would be a lie.
//   - A provider or model whose key is empty after trimming is dropped: it is
//     unaddressable, and keeping it would let Lookup("", "") succeed.
//   - Keys that collide after normalization ("OpenAI" and "openai") resolve
//     deterministically to the last one in sorted raw-key order, so two runs
//     over the same bytes always produce the same catalog.
func Parse(b []byte) (Catalog, error) {
	var raw map[string]Provider
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("modelcatalog: parse: %w", err)
	}
	return normalize(raw), nil
}

// Decode is Parse over a stream, for callers holding an io.Reader (an
// http.Response.Body during a snapshot refresh, an os.File in a test).
func Decode(r io.Reader) (Catalog, error) {
	var raw map[string]Provider
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("modelcatalog: decode: %w", err)
	}
	return normalize(raw), nil
}

// normalize applies the Parse contract. It is separate so Parse and Decode
// cannot drift apart.
func normalize(raw map[string]Provider) Catalog {
	out := make(Catalog, len(raw))
	for _, rawKey := range sortedKeys(raw) {
		key := canon(rawKey)
		if key == "" {
			continue
		}
		p := raw[rawKey]
		p.ID = key
		p.Models = normalizeModels(p.Models)
		out[key] = p
	}
	return out
}

func normalizeModels(raw map[string]Model) map[string]Model {
	if raw == nil {
		return nil
	}
	out := make(map[string]Model, len(raw))
	for _, rawKey := range sortedKeys(raw) {
		key := canon(rawKey)
		if key == "" {
			continue
		}
		m := raw[rawKey]
		m.ID = key
		out[key] = m
	}
	return out
}

// sortedKeys makes normalization order deterministic. Go map iteration is
// randomized, so without this a snapshot containing two keys that canonicalize
// to the same string would produce a different winner on each decode.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func canon(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Lookup resolves a (provider, model) pair. Both arguments are trimmed and
// lowercased, so the API enum form ("ANTHROPIC") and the Provider.Name() form
// ("anthropic") both land — the same case-insensitivity llm.CuratedModels and
// paymaster.lookupPrice already offer.
//
// The returned Model is a deep copy; mutating it (or its Modalities slices, or
// its Cost) cannot corrupt the shared catalog behind Default().
func (c Catalog) Lookup(provider, model string) (Model, bool) {
	p, ok := c[canon(provider)]
	if !ok {
		return Model{}, false
	}
	m, ok := p.Models[canon(model)]
	if !ok {
		return Model{}, false
	}
	return m.clone(), true
}

// Models returns a provider's models, newest first (ReleaseDate descending,
// then ID ascending so the order is total and stable for undated entries).
// That ordering matches how curatedModels is hand-maintained — most-capable
// and most-recent first, because a picker renders it top-to-bottom.
//
// nil means the provider is unknown. A known provider with no models returns
// an empty non-nil slice, so callers can tell "no such provider" from "that
// provider ships nothing". The result and every model in it are copies.
func (c Catalog) Models(provider string) []Model {
	p, ok := c[canon(provider)]
	if !ok {
		return nil
	}
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		out = append(out, m.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReleaseDate != out[j].ReleaseDate {
			return out[i].ReleaseDate > out[j].ReleaseDate
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Providers returns every provider id in the catalog, sorted. The slice is
// freshly allocated on each call.
func (c Catalog) Providers() []string {
	out := make([]string, 0, len(c))
	for id := range c {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// clone deep-copies the reference-typed fields so a caller cannot reach back
// into the shared catalog through a returned value.
func (m Model) clone() Model {
	out := m
	out.Modalities.Input = copyStrings(m.Modalities.Input)
	out.Modalities.Output = copyStrings(m.Modalities.Output)
	if m.Cost != nil {
		cost := *m.Cost
		if m.Cost.CacheRead != nil {
			v := *m.Cost.CacheRead
			cost.CacheRead = &v
		}
		if m.Cost.CacheWrite != nil {
			v := *m.Cost.CacheWrite
			cost.CacheWrite = &v
		}
		out.Cost = &cost
	}
	return out
}

func copyStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// DisplayName is the human label for the model: the published Name, falling
// back to the id when the snapshot has no name for it. Never empty for a model
// that came out of Parse, since Parse guarantees a non-empty ID.
func (m Model) DisplayName() string {
	if strings.TrimSpace(m.Name) == "" {
		return m.ID
	}
	return m.Name
}

// Rates flattens Cost into the four per-million channels paymaster prices
// separately. ok is false exactly when the model carries no cost block —
// an unpriced model must fall through to the caller's own fallback (the
// provider ceiling), never be billed at $0.
//
// A nil CacheRead or CacheWrite mirrors Input rather than resolving to zero.
// That is the convention the hand-written table already encodes for providers
// that publish no separate cache rate (xAI, OpenAI's cache_write): "not priced
// separately" means "priced as input". An explicit 0 in the snapshot survives
// as 0, because that one really does mean free.
func (m Model) Rates() (input, output, cacheRead, cacheWrite float64, ok bool) {
	if m.Cost == nil {
		return 0, 0, 0, 0, false
	}
	input = m.Cost.Input
	output = m.Cost.Output
	cacheRead = input
	if m.Cost.CacheRead != nil {
		cacheRead = *m.Cost.CacheRead
	}
	cacheWrite = input
	if m.Cost.CacheWrite != nil {
		cacheWrite = *m.Cost.CacheWrite
	}
	return input, output, cacheRead, cacheWrite, true
}
