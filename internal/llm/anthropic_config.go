package llm

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/llm/endpoint"
)

// AnthropicConfig parameterizes the Anthropic Messages codec: where it posts,
// how it authenticates, and which protocol headers travel with the request.
//
// Every field is optional. The zero AnthropicConfig is exactly today's
// hard-coded provider — api.anthropic.com, x-api-key auth, version 2023-06-01,
// the prompt-caching beta — which is what lets NewAnthropic stay a one-liner
// over NewAnthropicWith.
//
// Defaults are resolved LAZILY, by the accessors below, never at construction.
// A bare &Anthropic{apiKey: "..."} composite literal is a shape existing tests
// build directly, so a zero cfg has to behave like the default one rather than
// like a half-built object.
type AnthropicConfig struct {
	// Name is Provider.Name() and the lowercase wrap prefix in error strings
	// ("anthropic http: ..."). It is also the paymaster pricing key, so a value
	// with no rate row in internal/paymaster bills at $0. "" => "anthropic".
	Name string
	// DisplayName is the API-facing casing operators see in dashboards
	// ("Anthropic API returned 500: ..."). "" => "Anthropic".
	DisplayName string
	// BaseURL may be given in any shape — a bare root, ".../v1", or the full
	// ".../v1/messages" — because it is reduced to a mount root via
	// endpoint.Normalize and the API path is appended from there.
	// "" => anthropicAPIURL.
	BaseURL string
	APIKey  string
	// Version is the anthropic-version header. "" => "2023-06-01".
	Version string
	// Beta lists anthropic-beta feature flags, joined with ",". A nil slice
	// means "the default set" (prompt caching); a non-nil EMPTY slice means
	// "send no beta header at all", which is how a proxy that rejects unknown
	// betas is accommodated without inventing a second flag.
	Beta []string

	// Client is the SSRF seam: a caller wiring a tenant-configured endpoint
	// passes a client whose transport dials through the httpsafe fence. nil =>
	// a default client built from Timeout.
	Client *http.Client
	// Timeout bounds the whole request when Client is nil. 0 => 120s. Ignored
	// when Client is non-nil — a caller-supplied client owns its own deadline.
	Timeout time.Duration

	// --- Bedrock/Mantle seam ---
	//
	// Declared and honoured, but NOT exercised in Phase 1: no SigV4 code, no
	// AWS dependency, no Bedrock preset ships here. Bedrock speaks the same
	// Messages body and the same SSE event names, so the only things that
	// actually differ are the three knobs below. They exist now so the variant
	// is a config value later rather than a fork of this file.

	// Sign replaces the x-api-key header: when non-nil it is called with the
	// outgoing request and the body about to be sent (nil for GET /models), and
	// is responsible for all authentication headers. It receives the *http.Request
	// itself, so a transport that must not send anthropic-version can delete the
	// header here.
	Sign func(r *http.Request, body []byte) error
	// VersionInBody moves the API version from the anthropic-version header into
	// the request body as "anthropic_version", and omits the "model" key — Bedrock
	// addresses the model in the URL, and rejects a body that also names one.
	// false (the default) => today's body, byte for byte.
	VersionInBody bool
	// ModelInPath routes to "{root}/model/{id}/invoke" (or
	// ".../invoke-with-response-stream" when streaming) instead of
	// "{root}/v1/messages". false (the default) => the model and stream flag are
	// ignored and the Messages path comes back.
	ModelInPath bool
}

// NewAnthropicWith creates an Anthropic provider from an explicit config. The
// zero config yields the same provider NewAnthropic does.
func NewAnthropicWith(cfg AnthropicConfig) *Anthropic {
	client := cfg.Client
	if client == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		client = &http.Client{
			Timeout:   timeout,
			Transport: anthropicTransport(),
		}
	}
	// Normalize the EFFECTIVE base, not the raw one: endpoint.Normalize treats
	// an empty string as a parse error, and "unset" has to mean "the default
	// endpoint", not "a broken endpoint".
	ep, err := endpoint.Normalize(anthropicBase(cfg.BaseURL))
	return &Anthropic{
		apiKey: cfg.APIKey,
		client: client,
		cfg:    cfg,
		ep:     ep.WithWire(endpoint.WireAnthropicMessages),
		epErr:  err,
	}
}

// NewAnthropicWithClient creates an Anthropic provider whose requests use the
// supplied http.Client. This is the SSRF seam OpenAI and Ollama already have and
// Anthropic did not: a caller wiring an endpoint whose address did not come from
// this process passes a client dialling through the guarded transport.
//
// A nil client falls back to the default 120s client — callers wiring an
// untrusted endpoint MUST pass a guarded client, never nil.
func NewAnthropicWithClient(apiKey string, client *http.Client) *Anthropic {
	return NewAnthropicWith(AnthropicConfig{APIKey: apiKey, Client: client})
}

// anthropicTransport clones http.DefaultTransport rather than building a bare
// &http.Transport{}: a zero-value transport silently drops proxy support
// (HTTP_PROXY/HTTPS_PROXY) and every TLS/dial default (TLSHandshakeTimeout,
// ExpectContinueTimeout, IdleConnTimeout), which is how a deployment behind a
// corporate proxy lost Anthropic connectivity while Ollama — which clones —
// kept working.
//
// DisableCompression stays: SSE lines are small, so gzip adds latency, not value.
func anthropicTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	return tr
}

// anthropicBase resolves the configured base to a non-empty URL.
func anthropicBase(raw string) string {
	if b := strings.TrimSpace(raw); b != "" {
		return b
	}
	return anthropicAPIURL
}

// --- lazy accessors ---
//
// Each one falls back to the historical constant, so a zero Anthropic behaves
// exactly like NewAnthropic("") minus the client.

// name is the lowercase wrap prefix and the paymaster pricing key.
func (a *Anthropic) name() string {
	if a.cfg.Name == "" {
		return "anthropic"
	}
	return a.cfg.Name
}

// displayName is the API-facing casing in operator-visible errors.
func (a *Anthropic) displayName() string {
	if a.cfg.DisplayName == "" {
		return "Anthropic"
	}
	return a.cfg.DisplayName
}

// version is the anthropic-version value.
func (a *Anthropic) version() string {
	if a.cfg.Version == "" {
		return anthropicDefaultVersion
	}
	return a.cfg.Version
}

// betas is the anthropic-beta list. nil means the default set; a non-nil empty
// slice means no beta header, and is returned as-is so the caller can tell the
// two apart.
func (a *Anthropic) betas() []string {
	if a.cfg.Beta == nil {
		return []string{anthropicDefaultBeta}
	}
	return a.cfg.Beta
}

// baseURLError returns a parse error when the configured base is not a URL at
// all. Policy rejections (embedded credentials, an odd scheme) return nil — the
// raw value is attempted instead, because it may be a deployment that worked.
// Same discipline as OpenAI's: normalization may only repair a base, never
// reject one that used to work.
func (a *Anthropic) baseURLError() error {
	if a.epErr != nil && errors.Is(a.epErr, endpoint.ErrParse) {
		return fmt.Errorf("parse base url: %w", a.epErr)
	}
	return nil
}

// chatURL is the completion target. model and stream are consulted only by the
// ModelInPath seam; on the default path both are ignored and "{root}/v1/messages"
// comes back.
func (a *Anthropic) chatURL(model string, stream bool) string {
	if a.cfg.ModelInPath {
		suffix := "/invoke"
		if stream {
			suffix = "/invoke-with-response-stream"
		}
		// A Bedrock inference-profile id can carry a slash, so the model is one
		// escaped path SEGMENT, not a sub-path.
		return a.joinRoot("/model/"+model+suffix, "/model/"+url.PathEscape(model)+suffix)
	}
	if a.ep.Root == nil {
		return anthropicBase(a.cfg.BaseURL)
	}
	return a.ep.ChatURL()
}

// modelsURL is the discovery target. The mount prefix and any query string
// survive normalization.
//
// The fallback has to DERIVE the models path rather than hand back the raw base:
// the raw base is the chat target, so returning it verbatim would make ListModels
// POST-shaped queries at /v1/messages and parse whatever came back as a model
// list. An unset base short-circuits to the historical constant.
func (a *Anthropic) modelsURL() string {
	if a.ep.Root == nil {
		if strings.TrimSpace(a.cfg.BaseURL) == "" {
			return anthropicModelsURL
		}
		return rawAnthropicModelsURL(a.cfg.BaseURL)
	}
	return a.ep.ModelsURL()
}

// rawAnthropicModelsURL is the pre-normalization derivation for a base that
// could not be reduced: rewrite a trailing "/messages" to "/models". Going
// through net/url preserves the query, which a suffix trim on the whole string
// mangles.
func rawAnthropicModelsURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/messages") + "/models"
	return u.String()
}

// joinRoot appends a path to the normalized mount root, re-attaching any query
// the root carried. It exists because the Bedrock invoke path is not one of the
// wires endpoint.Endpoint builds; an unparseable base falls back to raw
// concatenation the way every other builder here does.
//
// The path is supplied twice — decoded and escaped — because url.URL re-escapes
// Path on its own, so handing it a pre-escaped segment yields "%252F". Setting
// RawPath alongside Path is the only way to keep a "%2F" inside one segment.
func (a *Anthropic) joinRoot(decodedPath, escapedPath string) string {
	if a.ep.Root == nil {
		return strings.TrimRight(anthropicBase(a.cfg.BaseURL), "/") + escapedPath
	}
	u := *a.ep.Root
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + escapedPath
	u.Path = strings.TrimRight(u.Path, "/") + decodedPath
	u.RawQuery = a.ep.Query
	return u.String()
}

// applyAuth writes the authentication headers. cfg.Sign, when set, replaces
// x-api-key entirely and owns whatever scheme the transport needs; body is the
// payload about to be sent, nil for a GET.
func (a *Anthropic) applyAuth(req *http.Request, body []byte) error {
	if a.cfg.Sign != nil {
		return a.cfg.Sign(req, body)
	}
	req.Header.Set("x-api-key", a.apiKey)
	return nil
}

// applyProtocolHeaders writes the version and beta headers common to every
// Anthropic call. An empty beta list omits the header rather than sending a
// blank one, which some proxies reject.
func (a *Anthropic) applyProtocolHeaders(req *http.Request) {
	req.Header.Set("anthropic-version", a.version())
	if betas := a.betas(); len(betas) > 0 {
		req.Header.Set("anthropic-beta", strings.Join(betas, ","))
	}
}
