package llm

import (
	"fmt"
	"strings"
)

// The provider registry: one table describing every provider this build can
// actually construct, replacing the switch statements that used to encode the
// same three-way choice in provider_build.go, keepercfg.auxProviders() and the
// error string that lists what an operator may type.
//
// The three copies drifted in exactly the way three copies do — the builder
// matched "anthropic" case-sensitively while internal/api carried the uppercase
// enum form, and the "want anthropic|openai|ollama" hint was a literal that
// nothing kept honest. A table that every one of them reads is the smallest
// change that makes adding a provider a single-site edit.
//
// Codec and Auth are descriptive in this phase: nothing dispatches on them yet,
// they are declared so the codec work landing alongside this has a place to say
// which wire format a provider speaks without a second table appearing.

// Codec names the wire format a provider speaks. Descriptive metadata today —
// the concrete constructor is ProviderSpec.New — and the discriminator a later
// phase dispatches on when the codecs become parameterized rather than one
// type each.
type Codec string

const (
	// CodecOpenAICompat is the /v1/chat/completions message model: flat
	// messages with tool_call_id on the tool-result turn.
	CodecOpenAICompat Codec = "openai-compat"
	// CodecAnthropicMessages is the /v1/messages model: content blocks, with
	// tool results carried as a block on a user turn.
	CodecAnthropicMessages Codec = "anthropic-messages"
	// CodecOllamaNative is Ollama's own /api/chat NDJSON protocol, which is
	// neither of the above and carries the think/format knobs Request exposes.
	CodecOllamaNative Codec = "ollama-native"
)

// AuthScheme names how a provider's credential reaches the wire. Like Codec it
// is descriptive in this phase; the constructors still apply their own headers.
type AuthScheme string

const (
	// AuthNone is a provider that takes no credential — a local runtime.
	AuthNone AuthScheme = "none"
	// AuthBearer sends "Authorization: Bearer <key>".
	AuthBearer AuthScheme = "bearer"
	// AuthAnthropicKey sends "x-api-key: <key>".
	AuthAnthropicKey AuthScheme = "x-api-key"
)

// ProviderSpec is one row of the registry: everything the aux-slot builder,
// the console's provider picker and the ledger's pricing key need to know
// about a provider, in one place.
//
// ID is load-bearing in three directions at once — it is what an AuxModel
// stores, what Provider.Name() returns, and the "<provider>/" half of the
// paymaster rate-card key. A row whose ID has no rate row in
// internal/paymaster/pricing.go bills every call at $0, so adding one is a
// two-file change by construction.
type ProviderSpec struct {
	// ID is the canonical lowercase identifier. REQUIRED.
	ID string
	// DisplayName is the human casing used in error messages ("OpenAI").
	// REQUIRED.
	DisplayName string
	// Codec is the wire format this provider speaks. REQUIRED.
	Codec Codec
	// Auth is how the credential reaches the wire. REQUIRED.
	Auth AuthScheme
	// KeyEnv is the environment variable the builder reads a missing API key
	// from. Empty means the provider needs no key at all (a local runtime),
	// and the builder then never errors for a missing one.
	KeyEnv string
	// BaseEnv is the environment variable holding an operator-set endpoint,
	// consulted when the caller passes no explicit base. Empty means the
	// provider is not endpoint-driven — we dial our own hosted API.
	BaseEnv string
	// BaseDefault is the endpoint used when neither the caller nor BaseEnv
	// supplies one. May be empty.
	BaseDefault string
	// CatalogID is this provider's id in the models.dev catalogue, for the
	// cases where it differs from ID. Empty means "same as ID", or — for a
	// runtime the catalogue has no entries for — that there is nothing to
	// look up.
	CatalogID string
	// DefaultAuxModel is the model an aux slot on this provider gets when the
	// operator named a provider and no model. Nothing reads it yet; it is the
	// seam the slot-fallback work needs so the default does not have to be a
	// second hardcoded literal next to DefaultAuxiliaryModels().
	DefaultAuxModel string
	// MaxTokensField is the output-limit key used by OpenAI-compatible codecs.
	// Empty keeps the compatibility default ("max_tokens"). Hosted OpenAI uses
	// "max_completion_tokens", the current Chat Completions API field.
	MaxTokensField string
	// New constructs the provider. REQUIRED. base and apiKey arrive already
	// resolved (explicit → env → default), so a New that ignores one is
	// simply a provider that does not take it.
	New func(m AuxModel, base, apiKey string) (Provider, error)
}

// providerRegistry and providerOrder are written ONLY by the func init() in
// this file, and read from the build path that sits behind every
// behavior-monitor call. There is no mutex, and that is deliberate: making
// registration concurrent-safe would put an RLock on the hot read for the
// benefit of a write that only happens at package init, before main runs.
//
// The rule this buys, therefore: RegisterProvider is init-only. Calling it
// after init from a goroutine racing a lookup is a data race, and the way to
// add a provider is a line in the init below, not a call from elsewhere.
var (
	providerRegistry = map[string]ProviderSpec{}
	providerOrder    []string
)

// RegisterProvider adds spec to the registry. Init-only — see the note on
// providerRegistry.
//
// It panics rather than returning an error because every failure it can see is
// a programming mistake in a package-level init: a missing required field or a
// duplicate id cannot be recovered from at runtime, and a provider that
// silently failed to register would surface much later as "unsupported aux
// provider" on an id the code plainly declares.
func RegisterProvider(spec ProviderSpec) {
	switch {
	case spec.ID == "":
		panic("llm: RegisterProvider: empty ID")
	case spec.DisplayName == "":
		panic(fmt.Sprintf("llm: RegisterProvider(%q): empty DisplayName", spec.ID))
	case spec.Codec == "":
		panic(fmt.Sprintf("llm: RegisterProvider(%q): empty Codec", spec.ID))
	case spec.Auth == "":
		panic(fmt.Sprintf("llm: RegisterProvider(%q): empty Auth", spec.ID))
	case spec.New == nil:
		panic(fmt.Sprintf("llm: RegisterProvider(%q): nil New", spec.ID))
	}
	if _, dup := providerRegistry[spec.ID]; dup {
		panic(fmt.Sprintf("llm: RegisterProvider(%q): duplicate provider id", spec.ID))
	}
	providerRegistry[spec.ID] = spec
	providerOrder = append(providerOrder, spec.ID)
}

// LookupProvider resolves id to its spec, lowercasing and trimming first.
//
// The normalization is a deliberate widening: internal/api carries the provider
// as an uppercase enum value and keepercfg stores it lowercase, so the old
// case-sensitive switch made "Anthropic" an unsupported provider on one side of
// a call and a working one on the other.
func LookupProvider(id string) (ProviderSpec, bool) {
	spec, ok := providerRegistry[strings.ToLower(strings.TrimSpace(id))]
	return spec, ok
}

// RegisteredProviders returns the provider ids in DECLARATION order, not
// sorted order.
//
// Declaration order because this list is served to the admin console's
// provider picker (internal/api/admin_keeper_aux.go, via
// keepercfg.AuxProviders) where it was previously a literal in the order
// anthropic, openai, ollama. Sorting would silently reorder an operator's
// dropdown for no reason anyone could point at.
func RegisteredProviders() []string {
	out := make([]string, len(providerOrder))
	copy(out, providerOrder)
	return out
}

// RegisteredProviderSpecs returns copies of every spec, in the same
// declaration order as RegisteredProviders. Copies, so a caller inspecting the
// table cannot edit the table.
func RegisteredProviderSpecs() []ProviderSpec {
	out := make([]ProviderSpec, 0, len(providerOrder))
	for _, id := range providerOrder {
		out = append(out, providerRegistry[id])
	}
	return out
}

// The built-in providers, in the order the console has always shown them.
//
// Each New calls the constructor that already existed, unchanged: this table
// is a re-description of the switch it replaces, not a behaviour change, and
// keeping it that way is what lets it land independently of the codec work
// happening in the same package.
func init() {
	RegisterProvider(ProviderSpec{
		ID:          "anthropic",
		DisplayName: "Anthropic",
		Codec:       CodecAnthropicMessages,
		Auth:        AuthAnthropicKey,
		KeyEnv:      "ANTHROPIC_API_KEY",
		// Hosted API, deliberately not endpoint-driven: the server key is
		// attached to this request, so the address it goes to is ours and not
		// an operator-supplied one.
		BaseDefault:     "https://api.anthropic.com/v1/messages",
		CatalogID:       "anthropic",
		DefaultAuxModel: HousekeepingModel("anthropic"),
		New: func(m AuxModel, base, apiKey string) (Provider, error) {
			return NewAnthropic(apiKey), nil
		},
	})
	RegisterProvider(ProviderSpec{
		ID:              "openai",
		DisplayName:     "OpenAI",
		Codec:           CodecOpenAICompat,
		Auth:            AuthBearer,
		KeyEnv:          "OPENAI_API_KEY",
		BaseDefault:     "https://api.openai.com/v1/chat/completions",
		CatalogID:       "openai",
		DefaultAuxModel: HousekeepingModel("openai"),
		MaxTokensField:  "max_completion_tokens",
		New: func(m AuxModel, base, apiKey string) (Provider, error) {
			return NewOpenAI(apiKey), nil
		},
	})
	RegisterProvider(ProviderSpec{
		ID:          "ollama",
		DisplayName: "Ollama",
		Codec:       CodecOllamaNative,
		Auth:        AuthNone,
		// No KeyEnv: the local judge's endpoint needs no credential, and
		// declaring one would turn a working local slot into a hard error the
		// moment the variable went unset.
		BaseEnv:     "KEEPER_OLLAMA_URL",
		BaseDefault: "http://localhost:11434",
		// The catalogue has no ollama entries — model ids there are whatever
		// the operator pulled — so there is nothing to look up.
		CatalogID:       "",
		DefaultAuxModel: "",
		New: func(m AuxModel, base, apiKey string) (Provider, error) {
			return NewOllama(base, m.Model), nil
		},
	})
}
