package llm

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

// defaultOpenAITimeout is the per-request deadline for the non-streaming
// client. It is deliberately NOT applied to the streaming client: see
// openAIStreamClient.
const defaultOpenAITimeout = 120 * time.Second

// noAuthPrefix is the documented sentinel for "send the raw key with no
// prefix at all". The zero value "" cannot mean that, because "" is what a
// caller who never thought about auth leaves behind and they must keep
// getting "Bearer " — so the empty-prefix case needs a value of its own.
// Azure's `api-key: <key>` is the shape that needs it.
const noAuthPrefix = "-"

// OpenAICompatConfig describes one OpenAI-Chat-Completions-compatible backend.
// Every knob has a zero value that reproduces today's hosted-OpenAI behaviour
// exactly, so an OpenAICompatConfig{} is a working OpenAI client minus the key.
//
// The message model — flat `tool_call_id`, `tools:[{type:"function"}]`,
// `finish_reason` — is shared by every backend this serves (OpenAI, DeepSeek,
// vLLM, llama.cpp, Ollama's /v1 shim). What differs between them is transport
// and vocabulary, which is what this struct carries; anything that differed in
// the message model would belong in a second codec, not here.
type OpenAICompatConfig struct {
	// Name is Provider.Name() and, through it, the paymaster pricing key.
	// A Name with no row in internal/paymaster's priceTable or
	// providerFallback bills every call at $0 — see the preset table below.
	// "" => "openai".
	Name string
	// DisplayName is the operator-facing casing used in error messages
	// ("invalid OpenAI API key"). "" => "OpenAI".
	DisplayName string
	// BaseURL may be given in any shape — a bare root, ".../v1", or the full
	// ".../v1/chat/completions" — because it is reduced to a mount root by
	// endpoint.Normalize at construction. "" => openaiAPIURL.
	BaseURL string
	APIKey  string
	// AuthHeader is the header the key is written to. "" => "Authorization".
	AuthHeader string
	// AuthPrefix is written immediately before the key. "" => "Bearer ";
	// the one-character sentinel "-" means "no prefix" (pair it with
	// AuthHeader:"api-key" for Azure). Any other value is used verbatim.
	AuthPrefix string
	// NoAuth suppresses the auth header entirely, for a local backend that
	// has no notion of a key. An empty APIKey does the same thing implicitly.
	NoAuth bool
	// Headers are extra static headers, applied BEFORE auth so they can
	// never clobber it (OpenRouter's HTTP-Referer/X-Title live here).
	Headers map[string]string

	// Client is the SSRF seam: a caller wiring a tenant-configured endpoint
	// passes a client whose transport dials through the httpsafe fence.
	// nil => a client built from Timeout.
	Client *http.Client
	// Timeout bounds a non-streaming request. 0 => 120s. Ignored when
	// Client != nil (the supplied client's own timeout governs).
	Timeout time.Duration
	// StreamClient overrides the derived streaming client verbatim. nil =>
	// derived: no total deadline, ResponseHeaderTimeout 60s.
	StreamClient *http.Client

	// IncludeUsage emits stream_options:{include_usage:true} on streamed
	// calls. Without it a streamed OpenAI response carries no usage block at
	// all, every call reports zero tokens, and paymaster prices it at $0.
	IncludeUsage bool
	// MaxTokensField is the body key the output cap is written to. "" =>
	// "max_tokens" (newer OpenAI models want "max_completion_tokens").
	MaxTokensField string
	// DefaultMaxTokens caps a request that did not ask for one. 0 => omit
	// the key entirely, which is what this provider has always done.
	DefaultMaxTokens int
	// ExtraBody is merged into the request body LAST and overwrites anything
	// it collides with, including "model" and "messages". It is the escape
	// hatch for a backend-specific key, not a general override point.
	ExtraBody map[string]any

	// StopReasons overlays the built-in finish_reason mapping. A key present
	// here wins; anything else falls through to the built-in map and then to
	// StopEndTurn.
	StopReasons map[string]StopReason
}

// withDefaults returns cfg with every zero field filled in. It is pure — no
// map is aliased or mutated — so it can be table-tested on its own, and the
// resolved config is what the provider stores, meaning the defaulting rules
// live in exactly one place.
func (c OpenAICompatConfig) withDefaults() OpenAICompatConfig {
	if c.Name == "" {
		c.Name = "openai"
	}
	if c.DisplayName == "" {
		c.DisplayName = "OpenAI"
	}
	if c.BaseURL == "" {
		c.BaseURL = openaiAPIURL
	}
	if c.AuthHeader == "" {
		c.AuthHeader = "Authorization"
	}
	switch c.AuthPrefix {
	case "":
		c.AuthPrefix = "Bearer "
	case noAuthPrefix:
		c.AuthPrefix = ""
	}
	if c.MaxTokensField == "" {
		c.MaxTokensField = "max_tokens"
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultOpenAITimeout
	}
	return c
}

// openAIStreamClient derives the client used by Stream. http.Client.Timeout
// covers body read, so applying the non-streaming 120s deadline to an SSE
// stream silently truncates any generation that runs longer — the same
// regression Ollama's split client exists to prevent. Dial and header time
// stay bounded by the transport; body read is left to caller ctx.
//
// A caller-supplied client's TRANSPORT is carried over (its timeout
// deliberately is not): a provider that is SSRF-fenced for Complete and open
// for Stream is one refactor away from dialling an arbitrary address, and the
// caller that asked for a guarded client would have no way to know.
func openAIStreamClient(cfg OpenAICompatConfig) *http.Client {
	if cfg.StreamClient != nil {
		return cfg.StreamClient
	}
	if cfg.Client != nil && cfg.Client.Transport != nil {
		// CheckRedirect travels with the transport. The governance client sets
		// http.ErrUseLastResponse alongside its dialer Control, and carrying
		// only the transport would leave Stream following up to ten redirects
		// off a fenced endpoint while Complete refuses at the first 3xx — the
		// asymmetry this function's comment says it exists to prevent.
		return &http.Client{
			Transport:     cfg.Client.Transport,
			CheckRedirect: cfg.Client.CheckRedirect,
		}
	}
	// Clone DefaultTransport so deployments behind HTTP_PROXY / HTTPS_PROXY
	// keep working and TLS/dial defaults aren't dropped — a zero-value
	// http.Transport silently disables all of those.
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}

// NewOpenAICompat builds a provider for any OpenAI-Chat-Completions-compatible
// backend. NewOpenAI, NewOpenAIWithBaseURL and NewOpenAIWithClient are all thin
// wrappers over it, so there is one construction path and one set of defaults.
func NewOpenAICompat(cfg OpenAICompatConfig) *OpenAI {
	return newOpenAIProvider(cfg.withDefaults())
}

// newOpenAIProvider builds the provider from an ALREADY-defaulted config. It
// exists so NewOpenAIWithBaseURL and NewOpenAIWithClient can default every
// other field while passing the caller's base URL through untouched —
// including an explicitly empty one, which must stay the parse error it has
// always been (endpoint_wiring_test.go's TestOpenAI_EmptyBaseIsAParseError).
// Silently rewriting an empty configured endpoint to api.openai.com would send
// a tenant's traffic somewhere they did not ask for, with their key attached.
func newOpenAIProvider(cfg OpenAICompatConfig) *OpenAI {
	ep, err := newOpenAIEndpoint(cfg.BaseURL)
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &OpenAI{
		apiKey:  cfg.APIKey,
		baseURL: cfg.BaseURL,
		ep:      ep,
		epErr:   err,
		client:  client,
		stream:  openAIStreamClient(cfg),
		cfg:     cfg,
	}
}

// openAIPresets are the OpenAI-compatible backends this package ships wiring
// for. The list is short on purpose: Name doubles as the paymaster pricing key
// and a Name with no rate row silently bills every call at $0
// (internal/paymaster/pricing.go), so a preset may only be added together with
// a verified price row. TestPresetsResolveToAPricedProvider enforces that.
//
// vllm carries no BaseURL because there is no such thing as a default vLLM
// address; a caller that leaves it empty gets api.openai.com from
// withDefaults, which is why the field is called out here rather than
// silently defaulted to a localhost guess.
var openAIPresets = map[string]OpenAICompatConfig{
	"openai": {
		Name: "openai", DisplayName: "OpenAI",
		BaseURL:      openaiAPIURL,
		IncludeUsage: true,
	},
	"deepseek": {
		Name: "deepseek", DisplayName: "DeepSeek",
		BaseURL:      "https://api.deepseek.com/v1",
		IncludeUsage: true,
	},
	"ollama-openai": {
		Name: "ollama", DisplayName: "Ollama",
		BaseURL:      "http://localhost:11434/v1",
		IncludeUsage: true,
		NoAuth:       true,
	},
	"vllm": {
		Name: "local", DisplayName: "vLLM",
		BaseURL:      "", // caller must set
		IncludeUsage: true,
		NoAuth:       true,
	},
}

// OpenAIPreset returns the un-defaulted config for a known backend, or
// (zero, false). The returned config is a copy the caller is expected to
// finish — at minimum APIKey, and BaseURL for vllm.
func OpenAIPreset(name string) (OpenAICompatConfig, bool) {
	cfg, ok := openAIPresets[strings.ToLower(strings.TrimSpace(name))]
	return cfg, ok
}

// OpenAIPresetNames returns the known preset keys, sorted.
func OpenAIPresetNames() []string {
	out := make([]string, 0, len(openAIPresets))
	for k := range openAIPresets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
