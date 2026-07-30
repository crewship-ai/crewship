// Package endpoint normalizes an operator-supplied model-server URL into a
// canonical root plus a wire format, and builds the concrete request URLs from
// that pair.
//
// Why this exists. A single ENDPOINT_URL credential used to be consumed by
// three code paths that each expected a DIFFERENT shape of the same string:
//
//	llm.Ollama          value + "/api/chat"          -> wants a bare root
//	OpenCode agent      value as OpenAI baseURL      -> wants ".../v1"
//	llm.OpenAI          value used verbatim as POST  -> wants ".../v1/chat/completions"
//
// and the reachability probe accepted all three, because it tried "/models" and
// then fell back to "/api/tags" on the root. So a credential stored in the shape
// our own documentation recommends (".../v1") made the Keeper judge POST to
// ".../v1/api/chat", get a 404, and — Keeper being fail-closed — DENY every
// credential request, while the Test button stayed green.
//
// The fix is to stop letting each consumer parse the raw string. Normalize once
// at the edge, keep the mount root, and let every caller ask for the URL of the
// wire it speaks. The failure mode is then structurally unreachable rather than
// merely unlikely.
//
// This package performs no I/O; see detect.go for the network-facing helpers.
package endpoint

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// maxRawLen mirrors the ENDPOINT_URL credential cap
// (internal/api/credentials_types.go maxEndpointValueLen). A value longer than
// this cannot have come from a supported path, and the stored form is inlined
// into agent env vars where an unbounded value is its own problem.
const maxRawLen = 2048

// Wire is the request/response protocol a model server speaks. It is stored
// alongside the endpoint because it cannot be reliably inferred from the URL —
// the same host:port routinely answers both Ollama-native and OpenAI-compatible
// requests, and which one to use is a deployment fact, not a syntactic one.
type Wire string

const (
	// WireOllama is Ollama's native chat API.
	WireOllama Wire = "ollama"
	// WireOpenAIChat is the OpenAI Chat Completions API — the lingua franca of
	// self-hosted inference servers (Ollama's compat layer, vLLM, LiteLLM, LM
	// Studio, llama.cpp server).
	WireOpenAIChat Wire = "openai-chat"
	// WireOpenAIResponses is OpenAI's newer Responses API. Codex CLI defaults to
	// it, which is why a Codex agent pointed straight at Ollama 404s unless the
	// provider is pinned to the chat wire.
	WireOpenAIResponses Wire = "openai-responses"
	// WireAnthropicMessages is Anthropic's Messages API. Claude Code speaks only
	// this shape, so a local model reaches it through a translating proxy
	// (LiteLLM and friends) rather than directly.
	WireAnthropicMessages Wire = "anthropic-messages"
)

// AllWires returns every supported wire, in a stable order suitable for
// rendering a picker.
func AllWires() []Wire {
	return []Wire{WireOllama, WireOpenAIChat, WireOpenAIResponses, WireAnthropicMessages}
}

// KnownWire reports whether s names a supported wire. Exported so the API layer
// validates operator input against the same set the URL builder trusts.
func KnownWire(s string) bool {
	for _, w := range AllWires() {
		if string(w) == s {
			return true
		}
	}
	return false
}

// ErrParse marks a value that is not a URL at all, as opposed to one this
// package rejects on policy (embedded credentials, a non-http scheme). Callers
// distinguish the two because a syntactically broken base was never going to
// work, while a policy-rejected one may be an odd deployment that did.
var ErrParse = errors.New("endpoint URL is not a valid URL")

// Endpoint is a normalized model-server target: the mount root, the wire spoken
// there, and any auth material that travels with it.
//
// Root never carries an API path segment or a query string; Query holds the
// latter so it can be re-attached to whichever path a wire needs (Azure-style
// deployments address by ?api-version=, and dropping it turns every call into a
// 400).
type Endpoint struct {
	Root  *url.URL
	Query string
	Wire  Wire
	// Versioned records whether this deployment mounts the OpenAI API under a
	// "/v1" segment. Almost every server does, so it defaults to true — but
	// Azure addresses deployments as ".../deployments/{name}/chat/completions"
	// with no version segment, and appending "/v1" there 404s. The flag is what
	// keeps one normalizer correct for both layouts.
	Versioned bool
	APIKey    string
	Headers   map[string]string
}

// apiSuffixes are the trailing path segments this package owns and will strip to
// recover the mount root. Multi-segment forms come first so "/v1/chat/completions"
// is consumed whole rather than leaving a dangling "/v1".
//
// Deliberately absent: the bare "/models", "/messages" and "/responses" forms. A
// reverse proxy legitimately mounts at those paths, and the versioned variants
// below already cover every real API layout, so stripping them would trade a
// fixed bug for an unfixable one.
var apiSuffixes = []string{
	"/v1/chat/completions",
	"/v1/responses",
	"/v1/messages",
	"/v1/models",
	"/chat/completions",
	"/api/generate",
	"/api/embeddings",
	"/api/version",
	"/api/embed",
	"/api/chat",
	"/api/tags",
	"/api/show",
	"/api/ps",
	"/v1",
	"/api",
}

// containerOnlyHosts resolve only from inside a container. They are correct for
// the agent path (which dials from a crew container) and wrong for anything
// dialling from the daemon — the same credential serves both, so the difference
// has to be caught rather than discovered as a timeout.
var containerOnlyHosts = map[string]bool{
	"host.docker.internal":     true,
	"gateway.docker.internal":  true,
	"host.containers.internal": true,
	"host.lima.internal":       true,
	"docker.for.mac.localhost": true,
	"docker.for.win.localhost": true,
}

// Normalize parses an operator-supplied endpoint into a canonical root.
//
// It accepts every shape someone plausibly pastes — a bare root, a trailing
// slash, ".../v1", a full ".../v1/chat/completions", an Ollama-native
// ".../api/chat", or a bare "host:port" — and reduces all of them to the same
// Root. A non-API path prefix is preserved, so an Ollama behind
// "https://gw.example.com/ollama" keeps its mount point.
//
// A value with no scheme is treated as http: that is what an operator means when
// they copy "192.168.1.40:11434" out of `ollama serve`, and https would simply
// fail to connect. Callers that care about cleartext (an endpoint carrying a
// token) enforce that separately — validateEndpointURL already does.
func Normalize(raw string) (Endpoint, error) {
	// Both of these are ErrParse, not policy rejections: callers fall back to
	// the raw value for a policy rejection, on the theory that an odd
	// deployment may have worked. Neither an empty string nor a multi-kilobyte
	// blob was ever a working endpoint, so falling back to it would trade a
	// clear "that is not a URL" for an obscure failure at request time.
	if len(raw) > maxRawLen {
		return Endpoint{}, fmt.Errorf("%w: endpoint URL is too long (max %d bytes)", ErrParse, maxRawLen)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Endpoint{}, fmt.Errorf("%w: endpoint URL is empty", ErrParse)
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return Endpoint{}, fmt.Errorf("%w: %w", ErrParse, err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return Endpoint{}, fmt.Errorf("endpoint URL must use the http or https scheme, got %q", u.Scheme)
	}
	if u.User != nil {
		return Endpoint{}, errors.New("endpoint URL must not embed credentials (user:pass@host); pass a token separately")
	}
	if u.Hostname() == "" {
		return Endpoint{}, errors.New("endpoint URL must include a host")
	}

	path, versioned := splitAPIPath(u.Path)
	root := &url.URL{
		Scheme: scheme,
		Host:   strings.ToLower(u.Host),
		Path:   path,
	}
	return Endpoint{Root: root, Query: u.RawQuery, Versioned: versioned}, nil
}

// splitAPIPath separates the mount root from the API suffix, and reports whether
// that suffix carried a version segment.
//
// Nothing stripped means we cannot tell, and "/v1" is overwhelmingly the norm —
// so the default is versioned. Only a suffix that was explicitly unversioned
// (".../chat/completions" with no "/v1" in front, the Azure deployments layout)
// turns it off.
func splitAPIPath(path string) (root string, versioned bool) {
	original := strings.TrimRight(path, "/")
	root = stripAPISuffixes(original)
	if root == original {
		return root, true
	}
	return root, strings.HasPrefix(strings.TrimPrefix(original, root), "/v1")
}

// stripAPISuffixes removes trailing API path segments until none match, so
// "/v1/v1" and "/openai/v1/chat/completions" both reduce correctly. The loop is
// bounded by the path shrinking on every iteration.
func stripAPISuffixes(path string) string {
	p := strings.TrimRight(path, "/")
	for {
		matched := false
		for _, suffix := range apiSuffixes {
			if strings.HasSuffix(p, suffix) {
				p = strings.TrimRight(strings.TrimSuffix(p, suffix), "/")
				matched = true
				break
			}
		}
		if !matched {
			return p
		}
	}
}

// versionPrefix is "/v1" for a versioned deployment and empty for one that
// mounts the API at its root (Azure deployments).
func (e Endpoint) versionPrefix() string {
	if e.Versioned {
		return "/v1"
	}
	return ""
}

// WithWire returns a copy of e bound to wire w. It does not mutate the receiver,
// so one normalized endpoint can be probed across several wires without probe
// order mattering.
func (e Endpoint) WithWire(w Wire) Endpoint {
	e.Wire = w
	return e
}

// ChatURL is the completion endpoint for e's wire.
func (e Endpoint) ChatURL() string {
	switch e.Wire {
	case WireOllama:
		return e.join("/api/chat")
	case WireOpenAIResponses:
		return e.join(e.versionPrefix() + "/responses")
	case WireAnthropicMessages:
		return e.join(e.versionPrefix() + "/messages")
	default: // WireOpenAIChat and anything unset — the safe lingua franca.
		return e.join(e.versionPrefix() + "/chat/completions")
	}
}

// ModelsURL is the model-listing endpoint for e's wire, used by discovery and by
// the "is that model actually pulled?" stage of the connection test.
func (e Endpoint) ModelsURL() string {
	if e.Wire == WireOllama {
		return e.join("/api/tags")
	}
	// Azure's classic layout addresses ONE deployment
	// (".../openai/deployments/{name}"), but the model list is a property of
	// the resource, not of a deployment: it answers at ".../openai/models".
	// Appending "/models" to the deployment root — which is what the chat
	// path correctly does — asks for a route Azure does not serve, so
	// discovery and the "is that model pulled?" probe both 404 against an
	// otherwise working endpoint.
	if root, ok := azureResourceRoot(e.Root); ok {
		e.Root = root
	}
	return e.join(e.versionPrefix() + "/models")
}

// azureResourceRoot drops a trailing "/deployments/{name}" from a root,
// reporting whether it found one. Only the classic layout has it; Azure's
// newer "/openai/v1" surface lists models at its own root and is untouched.
func azureResourceRoot(root *url.URL) (*url.URL, bool) {
	if root == nil {
		return nil, false
	}
	idx := strings.LastIndex(root.Path, "/deployments/")
	if idx < 0 {
		return nil, false
	}
	// Only when "/deployments/" is followed by a single deployment name —
	// a proxy that happens to mount UNDER a path containing more segments
	// after the name is not the layout this repairs.
	rest := strings.Trim(root.Path[idx+len("/deployments/"):], "/")
	if rest == "" || strings.Contains(rest, "/") {
		return nil, false
	}
	trimmed := *root
	trimmed.Path = root.Path[:idx]
	return &trimmed, true
}

// TagsURL is Ollama's native model list regardless of wire. The connection test
// probes it directly to tell "this is an Ollama" from "this is some other
// OpenAI-compatible server", which changes the advice we give the operator.
func (e Endpoint) TagsURL() string { return e.join("/api/tags") }

// ShowURL is Ollama's native per-model metadata endpoint — capabilities, context
// length and digest. Used at configure time to reject an embedding-only model as
// a judge and to pin the digest behind a mutable tag.
func (e Endpoint) ShowURL() string { return e.join("/api/show") }

// join appends an API path to the root, re-attaching any query the root carried.
func (e Endpoint) join(path string) string {
	if e.Root == nil {
		return ""
	}
	u := *e.Root
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = e.Query
	return u.String()
}

// String renders the root, which is the form to show an operator.
func (e Endpoint) String() string {
	if e.Root == nil {
		return ""
	}
	return e.Root.String()
}

// IsContainerOnlyHost reports whether the host resolves only from inside a
// container. Callers dialling from the daemon use it to refuse the value with an
// explanation instead of waiting out a DNS timeout.
func (e Endpoint) IsContainerOnlyHost() bool {
	if e.Root == nil {
		return false
	}
	return containerOnlyHosts[strings.ToLower(e.Root.Hostname())]
}
