package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/llm/endpoint"
)

const openaiAPIURL = "https://api.openai.com/v1/chat/completions"

// OpenAI implements Provider for the OpenAI Chat Completions API.
type OpenAI struct {
	apiKey  string
	baseURL string
	// ep is baseURL reduced to its mount root. Callers historically had to pass
	// the full ".../v1/chat/completions" because the value was used verbatim as
	// the POST target — so an ENDPOINT_URL credential stored in the ".../v1"
	// shape our own docs recommend 404'd. Normalizing lets any shape work.
	ep endpoint.Endpoint
	// epErr is why normalization failed, kept so a base that is not a URL at all
	// surfaces as a parse error rather than as a confusing request-construction
	// failure further down. A base rejected on policy (embedded credentials, an
	// odd scheme) is NOT an error here: it may be a deployment that worked, so it
	// falls through to the raw value.
	epErr  error
	client *http.Client
	// stream is the streaming client: same transport, no total deadline.
	// http.Client.Timeout covers body read, so reusing `client` silently
	// truncated any generation that outran it. See openAIStreamClient.
	stream *http.Client
	// cfg carries the backend-specific knobs (vocabulary, auth shape, extra
	// headers) already resolved through withDefaults. It sits ALONGSIDE the
	// fields above rather than replacing them: apiKey/baseURL/ep/epErr/client
	// are poked directly by the existing tests, and cfg.APIKey/cfg.BaseURL are
	// their construction-time copies, not a second source of truth.
	//
	// A zero-value OpenAI is not produced by any constructor, but the
	// accessors below still fall back to the hosted-OpenAI identity so one
	// could not silently send a bare key or an empty provider name.
	cfg OpenAICompatConfig
}

// name is Provider.Name() and the paymaster pricing key.
func (o *OpenAI) name() string {
	if o.cfg.Name == "" {
		return "openai"
	}
	return o.cfg.Name
}

// displayName is the operator-facing casing used in error messages.
func (o *OpenAI) displayName() string {
	if o.cfg.DisplayName == "" {
		return "OpenAI"
	}
	return o.cfg.DisplayName
}

// streamClient is the client Stream uses. It falls back to the bounded client
// only for a zero-value provider, which no constructor produces.
func (o *OpenAI) streamClient() *http.Client {
	if o.stream == nil {
		return o.client
	}
	return o.stream
}

// applyHeaders writes the configured static headers and then the auth header.
// Auth goes last so a Headers entry can never clobber it.
//
// An empty key sends NO auth header at all. It used to send a bare "Bearer ",
// which a local vLLM/llama.cpp server rejects outright — the one thing an
// unauthenticated backend cannot cope with is being offered an empty
// credential.
func (o *OpenAI) applyHeaders(req *http.Request) {
	for k, v := range o.cfg.Headers {
		req.Header.Set(k, v)
	}
	if o.cfg.NoAuth || o.apiKey == "" {
		return
	}
	header, prefix := o.cfg.AuthHeader, o.cfg.AuthPrefix
	if header == "" {
		// Zero-value provider: keep the historical Authorization: Bearer
		// shape rather than sending the key raw. withDefaults fills this in
		// for anything a constructor produced, so this is unreachable in
		// practice and deliberately cheap.
		header, prefix = "Authorization", "Bearer "
	}
	req.Header.Set(header, prefix+o.apiKey)
}

// baseURLError returns a parse error when the configured base is not a URL at
// all. Policy rejections return nil — the raw value is attempted instead.
func (o *OpenAI) baseURLError() error {
	if o.epErr != nil && errors.Is(o.epErr, endpoint.ErrParse) {
		return fmt.Errorf("parse base url: %w", o.epErr)
	}
	return nil
}

// newOpenAIEndpoint normalizes a base URL onto the chat wire. A parse failure
// leaves the endpoint zero, and the URL builders fall back to the raw value, so
// normalization can only repair a base — never reject one that used to work.
func newOpenAIEndpoint(baseURL string) (endpoint.Endpoint, error) {
	ep, err := endpoint.Normalize(baseURL)
	return ep.WithWire(endpoint.WireOpenAIChat), err
}

// chatURL is the completion target.
func (o *OpenAI) chatURL() string {
	if o.ep.Root == nil {
		return o.baseURL
	}
	return o.ep.ChatURL()
}

// modelsURL is the discovery target. The mount prefix and any query string
// (Azure addresses by ?api-version=) survive normalization, which is what the
// previous hand-rolled suffix rewrite was protecting.
//
// The fallback has to DERIVE the models path, not hand back the raw base: the
// raw base is the chat target, so returning it verbatim would make ListModels
// query /chat/completions and parse whatever came back as a model list. That
// path is reachable — a base rejected on policy (embedded credentials, an odd
// scheme) deliberately keeps working via the raw value — so it is the same
// suffix rewrite this package replaced, kept for exactly those deployments.
// Ollama's tagsURL fallback does the equivalent.
func (o *OpenAI) modelsURL() string {
	if o.ep.Root == nil {
		return rawModelsURL(o.baseURL)
	}
	return o.ep.ModelsURL()
}

// rawModelsURL is the pre-normalization derivation: rewrite a trailing
// "/chat/completions" to "/models". Going through net/url preserves the query
// (Azure's ?api-version=), which a suffix trim on the whole string mangles. An
// unparseable base has nothing to rewrite, so it is returned as-is and fails at
// request time the way it always did.
func rawModelsURL(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") + "/models"
	return u.String()
}

// NewOpenAI creates a provider that calls the OpenAI Chat Completions API.
func NewOpenAI(apiKey string) *OpenAI {
	return NewOpenAICompat(OpenAICompatConfig{
		APIKey:         apiKey,
		IncludeUsage:   true,
		MaxTokensField: "max_completion_tokens",
	})
}

// NewOpenAIWithBaseURL creates an OpenAI-compatible provider with a custom base URL.
// Useful for Azure OpenAI, local proxies, or other OpenAI-compatible APIs.
// An explicitly-empty baseURL is NOT rewritten to the hosted default: it is an
// unambiguous misconfiguration and keeps surfacing as a parse error, which is
// the only response that names the actual problem.
// stream_options is deliberately NOT set here. The hosted API accepts it, but
// this constructor takes an arbitrary endpoint: Azure OpenAI before
// api-version 2024-08-01 and several self-hosted servers reject unknown body
// keys outright ("Unrecognized request argument supplied: stream_options"), so
// forcing it on would break streaming that worked before this existed. Callers
// who know their backend supports it can opt in through NewOpenAICompat.
func NewOpenAIWithBaseURL(apiKey, baseURL string) *OpenAI {
	cfg := OpenAICompatConfig{APIKey: apiKey}.withDefaults()
	cfg.BaseURL = baseURL
	return newOpenAIProvider(cfg)
}

// NewOpenAIWithClient creates an OpenAI-compatible provider with a custom base
// URL AND a caller-supplied http.Client. This is the SSRF seam for a
// tenant-configured (openai_compat) governance-model endpoint (M2a, #1001): the
// caller passes a client whose transport dials through the #988 httpsafe fence,
// so a workspace can't point the endpoint at a link-local / private address to
// pivot. A nil client falls back to the default 120s client (same as
// NewOpenAIWithBaseURL) — callers wiring an untrusted endpoint MUST pass a
// guarded client, never nil.
func NewOpenAIWithClient(apiKey, baseURL string, client *http.Client) *OpenAI {
	// No stream_options — a tenant-configured endpoint is exactly the case
	// that may reject it. See NewOpenAIWithBaseURL.
	cfg := OpenAICompatConfig{APIKey: apiKey, Client: client}.withDefaults()
	cfg.BaseURL = baseURL // explicit, including "" — see NewOpenAIWithBaseURL
	return newOpenAIProvider(cfg)
}

// Name returns the configured provider id — "openai" for the hosted API, and
// whatever the compat config declared otherwise ("deepseek", "local", …).
// It is the paymaster pricing key, so it is never a display string.
func (o *OpenAI) Name() string { return o.name() }

// ListModels implements ModelLister against the OpenAI-compatible
// GET {root}/v1/models. baseURL may be given in any shape — a bare root,
// ".../v1", or the full ".../v1/chat/completions" — because it is reduced to a
// mount root at construction and the models path is appended from there.
func (o *OpenAI) ListModels(ctx context.Context) ([]ModelInfo, error) {
	if err := o.baseURLError(); err != nil {
		return nil, err
	}
	// The models path, the mount prefix and any query string (Azure addresses
	// by ?api-version=) all come from the normalized endpoint, so this no longer
	// has to hand-rewrite a suffix off the raw base.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, o.modelsURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	o.applyHeaders(httpReq)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s http: %w", o.name(), err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, o.displayName()); err != nil {
		return nil, err
	}

	var raw struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode %s models: %w", o.name(), err)
	}

	out := make([]ModelInfo, 0, len(raw.Data))
	for _, m := range raw.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, ModelInfo{ID: m.ID, DisplayName: m.ID, Provider: o.name()})
	}
	return out, nil
}

// Complete sends a non-streaming completion request to the OpenAI-compatible API.
func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := o.buildRequestBody(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := o.doWithRetry(ctx, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, o.displayName()); err != nil {
		return nil, err
	}

	var raw openaiResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", o.name(), err)
	}
	out := raw.toResponse()
	// toResponse resolves finish_reason through the built-in map only — its
	// signature is pinned by the existing tests, so the per-backend overlay is
	// applied here, where the config is in scope.
	if len(raw.Choices) > 0 {
		out.StopReason = o.stopReason(raw.Choices[0].FinishReason)
	}
	return out, nil
}

// Stream sends a streaming completion request, calling handler for each event.
func (o *OpenAI) Stream(ctx context.Context, req Request, handler func(StreamEvent) error) (*Response, error) {
	body, err := o.buildRequestBody(req, true)
	if err != nil {
		return nil, err
	}
	resp, err := o.do(ctx, o.streamClient(), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := checkStatus(resp, o.displayName()); err != nil {
		return nil, err
	}

	return o.parseSSEStream(resp.Body, handler)
}

// doWithRetry executes an HTTP request with exponential backoff retry on transient errors.
// Shares the loop in httpretry.go with the Anthropic provider (max 3 attempts, 1s/2s/4s
// exponential backoff with jitter, Retry-After honoured, retryableStatusCodes) so policy
// stays consistent across LLM backends. Without this the caller saw raw 429/503 from the
// upstream the moment OpenAI rate-limited a burst -- which the orchestrator's own retry
// layer would then duplicate, amplifying spikes.
func (o *OpenAI) doWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	return o.do(ctx, o.client, body)
}

// do is doWithRetry with the client made explicit, so Stream can run on the
// deadline-free streaming client while Complete keeps its 120s safety net.
func (o *OpenAI) do(ctx context.Context, client *http.Client, body []byte) (*http.Response, error) {
	if client == nil {
		client = o.client
	}
	return doWithRetry(ctx, client, func(ctx context.Context) (*http.Request, error) {
		return o.newHTTPRequest(ctx, body)
	}, o.name(), o.displayName())
}

func (o *OpenAI) newHTTPRequest(ctx context.Context, body []byte) (*http.Request, error) {
	// Wrapped as a request-build failure because that is what it is from the
	// retry loop's point of view — it must not retry a base URL that will never
	// parse.
	if err := o.baseURLError(); err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.chatURL(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	o.applyHeaders(req)
	return req, nil
}

// maxTokensField is the body key the output cap is written to. Newer OpenAI
// models want "max_completion_tokens"; everything else still takes
// "max_tokens", which stays the default.
func (o *OpenAI) maxTokensField() string {
	if o.cfg.MaxTokensField == "" {
		return "max_tokens"
	}
	return o.cfg.MaxTokensField
}

func (o *OpenAI) buildRequestBody(req Request, stream bool) ([]byte, error) {
	msgs := make([]openaiMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openaiMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, toOpenAIMessage(m))
	}

	body := map[string]any{
		"model":    req.Model,
		"messages": msgs,
	}
	switch {
	case req.MaxTokens > 0:
		body[o.maxTokensField()] = req.MaxTokens
	case o.cfg.DefaultMaxTokens > 0:
		body[o.maxTokensField()] = o.cfg.DefaultMaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if len(req.Tools) > 0 {
		body["tools"] = toFunctionTools(req.Tools)
	}
	if stream {
		body["stream"] = true
		// Without stream_options a streamed response carries no usage block
		// at all: every streamed call reports zero tokens and paymaster
		// prices it at $0. It is only valid on a streamed request — sending
		// it on a non-streamed one is a 400 on real OpenAI.
		if o.cfg.IncludeUsage {
			body["stream_options"] = map[string]any{"include_usage": true}
		}
	}
	// Merged last, and deliberately allowed to overwrite: ExtraBody is the
	// escape hatch for a backend that needs a key this codec does not model,
	// which includes replacing one it does.
	for k, v := range o.cfg.ExtraBody {
		body[k] = v
	}
	return json.Marshal(body)
}

// --- OpenAI wire types ---

type openaiMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// functionToolDef is the OpenAI-style {"type":"function","function":{...}}
// tool wire format. Ollama adopted the same schema, so both providers share
// this struct and the toFunctionTools converter.
type functionToolDef struct {
	Type     string `json:"type"`
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters"`
	} `json:"function"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openaiToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			// OpenAI auto-caches prompts ≥1024 tokens since Sept 2025;
			// cached_tokens is the read count (no separate "creation"
			// counter — caching is opaque on their side).
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// openAIStopReasonTable maps every finish_reason this codec has seen in the
// wild. "tool_calls" and "length" are what hosted OpenAI sends; "function_call"
// is the deprecated form that several compat backends still emit, "tool_use"
// is what an Anthropic-shim proxy passes through, and "max_tokens" is vLLM's
// spelling. Until they were listed here a backend using any of the three
// accumulated its tool calls correctly and then reported end_turn — an agent
// loop reading StopReason would stop mid-turn with tool calls unanswered.
var openAIStopReasonTable = map[string]StopReason{
	"tool_calls":    StopToolUse,
	"function_call": StopToolUse,
	"tool_use":      StopToolUse,
	"length":        StopMaxToks,
	"max_tokens":    StopMaxToks,
	"stop":          StopEndTurn,
}

// openAIStopReason is the built-in mapping, with StopEndTurn as the default:
// an unknown terminal reason is a finished turn, not a failure.
func openAIStopReason(s string) StopReason {
	if r, ok := openAIStopReasonTable[s]; ok {
		return r
	}
	return StopEndTurn
}

// stopReason is openAIStopReason with the per-backend overlay applied on top,
// for a backend whose vocabulary collides with the built-in one.
func (o *OpenAI) stopReason(s string) StopReason {
	if r, ok := o.cfg.StopReasons[s]; ok {
		return r
	}
	return openAIStopReason(s)
}

// freshPromptToks reduces OpenAI's prompt_tokens to the fresh (uncached) part
// of the prompt. OpenAI's `prompt_tokens` INCLUDES cached_tokens; feeding it to
// paymaster.Estimate verbatim bills the cached read twice — once at the full
// input rate and again at the cached rate. internal/sidecar/usage.go:122 has
// always done this subtraction on the proxy billing path, so taking the wire
// value here made the two writers of cost_ledger disagree.
//
// The clamp is not defensive tidiness: an OpenAI-compatible backend (the vllm
// and ollama-openai presets) that reports cached_tokens > prompt_tokens would
// otherwise store a NEGATIVE cost_ledger.input_tokens. Estimate's own clamp
// protects the dollar figure, not the stored count, and a negative count
// inverts the cache-hit ratio gate in paymaster/ledger.go.
//
// Note the asymmetry with anthropic.go, which is correct as it stands:
// Anthropic reports input_tokens and cache_read_input_tokens as disjoint
// counts, so subtracting there would under-bill every cache read.
func freshPromptToks(prompt, cached int) int {
	if fresh := prompt - cached; fresh > 0 {
		return fresh
	}
	return 0
}

func (r *openaiResponse) toResponse() *Response {
	resp := &Response{
		InputToks:       freshPromptToks(r.Usage.PromptTokens, r.Usage.PromptTokensDetails.CachedTokens),
		OutputToks:      r.Usage.CompletionTokens,
		CachedInputToks: r.Usage.PromptTokensDetails.CachedTokens,
	}
	if len(r.Choices) == 0 {
		resp.StopReason = StopEndTurn
		return resp
	}
	choice := r.Choices[0]
	resp.StopReason = openAIStopReason(choice.FinishReason)
	resp.Content = choice.Message.Content
	for _, tc := range choice.Message.ToolCalls {
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: tc.Function.Arguments,
		})
	}
	return resp
}

func toOpenAIMessage(m Message) openaiMessage {
	if m.Role == RoleTool {
		return openaiMessage{
			Role:       "tool",
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
	}
	if len(m.ToolCalls) > 0 {
		tcs := make([]openaiToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			tcs[i] = openaiToolCall{
				ID:   tc.ID,
				Type: "function",
			}
			tcs[i].Function.Name = tc.Name
			tcs[i].Function.Arguments = tc.Input
		}
		return openaiMessage{
			Role:      "assistant",
			Content:   m.Content,
			ToolCalls: tcs,
		}
	}
	return openaiMessage{Role: m.Role, Content: m.Content}
}

func toFunctionTools(tools []ToolDef) []functionToolDef {
	out := make([]functionToolDef, len(tools))
	for i, t := range tools {
		out[i] = functionToolDef{Type: "function"}
		out[i].Function.Name = t.Name
		out[i].Function.Description = t.Description
		out[i].Function.Parameters = t.InputSchema
	}
	return out
}

func (o *OpenAI) parseSSEStream(r io.Reader, handler func(StreamEvent) error) (*Response, error) {
	final := &Response{StopReason: StopEndTurn}
	var textParts []string

	type partialToolCall struct {
		ID   string
		Name string
		Args strings.Builder
	}
	toolMap := make(map[int]*partialToolCall)

	// sawFinishReason tracks whether any choice ever carried a non-empty
	// finish_reason -- OpenAI's terminal signal, always sent on the last
	// chunk before "[DONE]". A connection can also close cleanly (plain
	// io.EOF, scanner.Err() == nil) mid-generation, which otherwise looked
	// identical to a normal completion; see the check after the scan loop.
	//
	// finish_reason isn't the only valid terminal signal, though: "[DONE]" on
	// its own is a positive end-of-stream marker per the Chat Completions SSE
	// protocol, and some OpenAI-compatible backends (self-hosted vLLM,
	// llama.cpp, LM Studio, TGI, LocalAI -- reachable via the openai_compat
	// governance path, NewOpenAIWithClient) send it without ever populating
	// finish_reason on a chunk. sawDoneSentinel (forEachSSEData's return)
	// covers that case so a compliant-but-terse backend isn't mistaken for a
	// truncated one.
	var sawFinishReason bool

	sawDoneSentinel, fnErr, scanErr := forEachSSEData(r, 64*1024, 1024*1024, func(data string) (bool, error) {
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return false, nil
		}
		if chunk.Usage != nil {
			// Recomputed from THIS chunk's own pair, never subtracted from
			// the already-stored final.InputToks: a backend that repeats the
			// usage block on more than one chunk would otherwise subtract the
			// cached count twice.
			final.InputToks = freshPromptToks(chunk.Usage.PromptTokens, chunk.Usage.PromptTokensDetails.CachedTokens)
			final.OutputToks = chunk.Usage.CompletionTokens
			final.CachedInputToks = chunk.Usage.PromptTokensDetails.CachedTokens
		}

		if len(chunk.Choices) == 0 {
			return false, nil
		}
		choice := chunk.Choices[0]

		if choice.Delta.Content != "" {
			textParts = append(textParts, choice.Delta.Content)
			if err := handler(StreamEvent{Type: "text", Content: choice.Delta.Content}); err != nil {
				return false, err
			}
		}

		for _, tcDelta := range choice.Delta.ToolCalls {
			ptc, ok := toolMap[tcDelta.Index]
			if !ok {
				ptc = &partialToolCall{}
				toolMap[tcDelta.Index] = ptc
			}
			if tcDelta.ID != "" {
				ptc.ID = tcDelta.ID
			}
			if tcDelta.Function.Name != "" {
				ptc.Name = tcDelta.Function.Name
			}
			ptc.Args.WriteString(tcDelta.Function.Arguments)
		}

		if choice.FinishReason != "" {
			sawFinishReason = true
			final.StopReason = o.stopReason(choice.FinishReason)
		}
		return false, nil
	})
	if fnErr != nil {
		return final, fnErr
	}

	final.Content = strings.Join(textParts, "")
	// Iterate the map's own keys in order, not 0..len-1. The counting loop that
	// used to be here assumed every backend numbers its tool_calls contiguously
	// from zero: with indices {0, 2} it read len==2, walked i=0 and i=1, and
	// never reached the call at index 2 — silently emitting one tool call where
	// the model asked for two. Hosted OpenAI numbers contiguously so it never
	// showed there, but the vLLM and Mistral-compat builds start at 1 or leave
	// gaps, and this package now ships a vllm preset and a `provider check
	// --provider vllm` path. A dropped tool call in an agent loop is not a
	// visible failure; it is the model appearing to decline to act.
	indices := make([]int, 0, len(toolMap))
	for i := range toolMap {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		ptc := toolMap[i]
		if ptc == nil {
			continue
		}
		tc := ToolCall{ID: ptc.ID, Name: ptc.Name, Input: ptc.Args.String()}
		final.ToolCalls = append(final.ToolCalls, tc)
		if err := handler(StreamEvent{Type: "tool_call", ToolCall: &tc}); err != nil {
			return final, err
		}
	}

	// The scan loop ended without an fnErr. If it also ended without a real
	// scanner error, that normally means a clean io.EOF -- which is exactly
	// what a legitimate finish looks like AND exactly what a mid-generation
	// connection drop looks like. A real completion always carries EITHER a
	// finish_reason on its last chunk OR the "[DONE]" sentinel (real OpenAI
	// sends both; some OpenAI-compatible backends send only "[DONE]" without
	// ever populating finish_reason) -- either one is proof the stream ended
	// on purpose, not mid-flight. Without this, a cut stream silently
	// returned nil error with truncated content as if it were a full
	// response.
	//
	// A genuine scanErr (including context.Canceled from a caller-initiated
	// cancellation) is left untouched here and returned as-is below, so
	// cancellation semantics are unchanged.
	if scanErr == nil && !sawFinishReason && !sawDoneSentinel {
		scanErr = fmt.Errorf("openai stream ended without a finish_reason (connection closed unexpectedly after %d bytes of content): %w", len(final.Content), io.ErrUnexpectedEOF)
	}

	if err := handler(StreamEvent{Type: "done", Response: final}); err != nil {
		return final, err
	}
	return final, scanErr
}
