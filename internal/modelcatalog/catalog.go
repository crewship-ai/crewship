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
// open_weights, last_updated, cost.reasoning, cost.input_audio, …) decode away
// silently, so a snapshot refresh that adds upstream fields never breaks the
// build.
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
//
// Tiers holds the rate card the provider switches to above a context
// threshold. The four scalar fields above are the BASE block — what a call
// below the first threshold costs — so a model with tiers is priced by two
// numbers, not one, and a consumer that reads only the scalars under-bills
// every long-context call. See RatesAt and CeilingRates.
//
// cost.context_over_200k is deliberately NOT decoded. It is a denormalized
// duplicate of one Tiers entry with the "tier" key removed, and its name lies:
// for 34 of the 65 models that carry it the real threshold is 256,000 or
// 272,000 tokens, not 200,000. Wiring the key name would over-bill every
// 200k–272k call on the bedrock/openai gpt-5.6 family. Tiers is the
// authoritative form; the legacy key is ignored on purpose.
//
// cost.reasoning, cost.input_audio and cost.output_audio are also left
// undecoded: nothing upstream of this package counts reasoning or audio
// tokens, so a decoded rate would have nothing to multiply by, and a rate with
// no counter is dead weight that invites a wrong bill later.
type Cost struct {
	Input      float64    `json:"input"`
	Output     float64    `json:"output"`
	CacheRead  *float64   `json:"cache_read,omitempty"`
	CacheWrite *float64   `json:"cache_write,omitempty"`
	Tiers      []CostTier `json:"tiers,omitempty"`
}

// CostTier is one rate card that replaces the base block once a call crosses
// its threshold. Same four channels, same USD-per-million unit, same
// pointer-means-absent convention — a tier that publishes no cache_read
// mirrors THAT TIER's input, not the base input. Mirroring the base would
// under-bill every cache read on a tier that doubled its rate.
type CostTier struct {
	Input      float64   `json:"input"`
	Output     float64   `json:"output"`
	CacheRead  *float64  `json:"cache_read,omitempty"`
	CacheWrite *float64  `json:"cache_write,omitempty"`
	Tier       TierBound `json:"tier"`
}

// TierBound is the threshold a CostTier applies above.
//
// Type is the axis. Every tier in every snapshot we have seen is "context" —
// the total prompt size — and normalize drops anything else rather than
// guessing: an unknown axis applied as though it were context would reprice a
// call on a rule nobody wrote.
//
// Size is that axis's threshold in tokens, exclusive. models.dev names these
// tiers "over 200k", so a call at exactly Size is still base-priced.
type TierBound struct {
	Type string `json:"type"`
	Size int64  `json:"size"`
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
//   - Cost.Tiers is sorted ascending by threshold, and tiers on an axis other
//     than "context" — or with a non-positive threshold, which is not a
//     boundary — are dropped. Doing it here means RatesAt never has to trust
//     upstream ordering, and an unknown tier axis can never silently reprice a
//     call.
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
		m.Cost = normalizeCost(m.Cost)
		out[key] = m
	}
	return out
}

// normalizeCost applies the tier half of the Parse contract: drop tiers this
// package cannot price, then sort what is left ascending by threshold.
//
// A tier is dropped when its axis is not "context" (the only axis models.dev
// publishes today, and the only one anything downstream can measure) or when
// its threshold is not positive (a "tier" that applies above zero tokens is not
// a tier — it would shadow the base block on every call).
//
// Both drops cost accuracy, and that is accepted rather than unnoticed: a
// dropped tier is a rate we will not charge, so a model priced entirely on an
// axis we do not understand falls back to its base block. Applying the
// threshold anyway would reprice calls on a rule nobody wrote, in a direction
// nobody can predict. If a second axis ever appears upstream, the fix is to
// teach RatesAt to measure it — not to let it through here.
func normalizeCost(c *Cost) *Cost {
	if c == nil || len(c.Tiers) == 0 {
		return c
	}
	kept := make([]CostTier, 0, len(c.Tiers))
	for _, t := range c.Tiers {
		if canon(t.Tier.Type) != tierTypeContext || t.Tier.Size <= 0 {
			continue
		}
		kept = append(kept, t)
	}
	// Stable so two tiers sharing a threshold keep their published order and
	// the catalog stays byte-identical across decodes.
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Tier.Size < kept[j].Tier.Size })
	if len(kept) == 0 {
		kept = nil
	}
	c.Tiers = kept
	return c
}

// tierTypeContext is the only tier axis in any snapshot we have seen: the
// threshold is the total prompt size in tokens.
const tierTypeContext = "context"

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
		if m.Cost.Tiers != nil {
			cost.Tiers = make([]CostTier, len(m.Cost.Tiers))
			for i, t := range m.Cost.Tiers {
				cost.Tiers[i] = t
				if t.CacheRead != nil {
					v := *t.CacheRead
					cost.Tiers[i].CacheRead = &v
				}
				if t.CacheWrite != nil {
					v := *t.CacheWrite
					cost.Tiers[i].CacheWrite = &v
				}
			}
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
//
// Rates returns the BASE block: what the model costs below its first context
// threshold. It is RatesAt(0), kept as its own name because most callers ask
// "what does this model cost" without a call in hand. A caller that has a real
// prompt size must use RatesAt, and a caller that has none but is going to
// BILL with the answer must use CeilingRates — on a tiered model this number
// under-bills, by up to 6.7× on the snapshot's worst case
// (openrouter/qwen/qwen3.7-flash, 0.03 → 0.20 above 256k).
func (m Model) Rates() (input, output, cacheRead, cacheWrite float64, ok bool) {
	return m.RatesAt(0)
}

// RatesAt is Rates for a call whose total prompt is contextToks tokens —
// fresh + cached + cache-creation input, which is the axis providers actually
// tier on (TierBound.Type is always "context").
//
// The threshold is exclusive: a tier applies when contextToks is strictly
// greater than its Size, because models.dev names these tiers "over 200k". A
// call at exactly 200,000 tokens is base-priced.
//
// Selection is by the largest threshold below contextToks rather than by slice
// position, so a hand-built Model whose Tiers never went through Parse
// resolves the same way a decoded one does. The nil-mirror rule is applied
// AFTER selection, against the selected tier's own input.
func (m Model) RatesAt(contextToks int64) (input, output, cacheRead, cacheWrite float64, ok bool) {
	if m.Cost == nil {
		return 0, 0, 0, 0, false
	}
	sel := baseBlock(m.Cost)
	best := int64(-1)
	for _, t := range m.Cost.Tiers {
		if contextToks > t.Tier.Size && t.Tier.Size > best {
			sel, best = tierBlock(t), t.Tier.Size
		}
	}
	input, output, cacheRead, cacheWrite = sel.resolve()
	return input, output, cacheRead, cacheWrite, true
}

// CeilingRates is the most expensive rate card the model publishes: the base
// block, or whichever tier prices input highest.
//
// It exists for the caller that must produce a bill without knowing the prompt
// size. paymaster.Estimate is exactly that caller — it receives token counts
// but no context axis, and changing its signature is a separate change — so
// catalog_pricing.go bills tiered models at their ceiling. Over-estimating
// keeps the budget warn/exceed signal firing when it should; under-estimating
// silently spends money the operator was told they had. That is the same
// reasoning providerFallback already encodes ("the most-expensive known tier
// for that provider, not the median"), and the hand-written table reached the
// same answer independently: priceTable["google/gemini-2.5-pro"] carries
// 2.50/15.00, which is this model's upper tier, not its 1.25/10.00 base.
//
// The row returned is always ONE published tier, never a per-channel maximum
// assembled from several. A synthetic worst-of-every-column row would describe
// a price no provider charges, and it would drift further from the invoice than
// the tier it replaced. Ties on input break toward the higher output rate. In
// the committed snapshot the highest-input tier is also the highest-output tier
// for all 76 tiered models, so the choice of key is not load-bearing today —
// it is pinned by a test so it stays that way visibly.
func (m Model) CeilingRates() (input, output, cacheRead, cacheWrite float64, ok bool) {
	if m.Cost == nil {
		return 0, 0, 0, 0, false
	}
	sel := baseBlock(m.Cost)
	for _, t := range m.Cost.Tiers {
		b := tierBlock(t)
		if b.input > sel.input || (b.input == sel.input && b.output > sel.output) {
			sel = b
		}
	}
	input, output, cacheRead, cacheWrite = sel.resolve()
	return input, output, cacheRead, cacheWrite, true
}

// rateBlock is one un-mirrored rate card — the base block or a tier — so the
// three accessors above share a single copy of the nil-mirror rule instead of
// each re-deriving it.
type rateBlock struct {
	input, output         float64
	cacheRead, cacheWrite *float64
}

func baseBlock(c *Cost) rateBlock {
	return rateBlock{input: c.Input, output: c.Output, cacheRead: c.CacheRead, cacheWrite: c.CacheWrite}
}

func tierBlock(t CostTier) rateBlock {
	return rateBlock{input: t.Input, output: t.Output, cacheRead: t.CacheRead, cacheWrite: t.CacheWrite}
}

// resolve applies the nil-mirror convention: an absent cache channel bills at
// THIS block's input rate. Mirroring some other block's input is the bug this
// method exists to make impossible.
func (b rateBlock) resolve() (input, output, cacheRead, cacheWrite float64) {
	input, output = b.input, b.output
	cacheRead, cacheWrite = input, input
	if b.cacheRead != nil {
		cacheRead = *b.cacheRead
	}
	if b.cacheWrite != nil {
		cacheWrite = *b.cacheWrite
	}
	return input, output, cacheRead, cacheWrite
}
