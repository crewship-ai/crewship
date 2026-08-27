package main

// `crewship provider check` — the smallest live proof that a provider is wired
// correctly: build the real codec from the real registry, send one completion,
// and print what came back next to what paymaster will bill for it.
//
// It exists because every other route to that answer is indirect and late. A
// mis-configured aux slot surfaces as a Keeper verdict that fails closed hours
// later; a base URL pointing at the wrong mount surfaces as a 404 buried in a
// server log; a backend that omits its usage block surfaces as a $0 cost line
// nobody questions until the invoice arrives. This turns all three into one
// line on a terminal, before anything is saved.
//
// Local by construction — no API client, no token, no workspace. The request
// leaves THIS machine, which is also what makes it the right tool for a local
// Ollama the server cannot reach:
//
//	crewship provider check --provider ollama-openai --model qwen2.5:3b
//
// A key is required only when the endpoint is the provider's own hosted API.
// Point --base-url somewhere else and the check runs without one, because that
// is exactly the keyless-local-backend case, and demanding OPENAI_API_KEY to
// reach localhost:11434 would be a refusal with no reason behind it.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/paymaster"
)

const (
	// defaultCheckPrompt is deliberately trivial. The command answers "is the
	// plumbing connected", not "is the model any good", and a one-word answer
	// keeps the check cheap enough to run against a metered provider without
	// thinking about it.
	defaultCheckPrompt = "Reply with the single word: pong."
	// checkMaxTokens caps the reply. A truncated answer still proves every
	// link in the chain — auth, endpoint, model id, usage accounting — and the
	// truncation is reported honestly as stop_reason=max_tokens rather than
	// hidden, so a cap that got in the way is visible.
	checkMaxTokens = 64
	// defaultCheckTimeout bounds the whole call via context. The codecs carry
	// their own client timeouts (120s for OpenAI, 300s for Ollama), which are
	// right for production work and far too patient for a diagnostic: an
	// operator running this wants to know it is broken now, not in five
	// minutes.
	defaultCheckTimeout = 60 * time.Second
)

// checkOptions is the flag set, resolved into one value so the construction
// path can be table-tested without a cobra command.
type checkOptions struct {
	Provider string
	BaseURL  string
	Model    string
	APIKey   string
}

// CheckTarget records how the provider was built. It is resolved BEFORE the
// call and printed even when the call fails, because "which endpoint did it
// actually dial, and did it find a key" is most of the diagnosis.
type CheckTarget struct {
	// Provider is what the operator typed, normalized. It may be a registry id
	// or an openai-compat preset name.
	Provider string `json:"provider" yaml:"provider"`
	// PricingKey is Provider.Name() — the "<provider>/" half of the paymaster
	// rate-card key, which is NOT always the flag value: the ollama-openai
	// preset prices as "ollama" and vllm as "local".
	PricingKey string `json:"pricing_key" yaml:"pricing_key"`
	Codec      string `json:"codec" yaml:"codec"`
	Endpoint   string `json:"endpoint" yaml:"endpoint"`
	Model      string `json:"model" yaml:"model"`
	// KeySource says where the credential came from: "--api-key", "$ENVVAR",
	// or "none". Never the key itself.
	KeySource string `json:"key_source" yaml:"key_source"`
}

// providerCheckResult is the JSON contract an agent parses.
type providerCheckResult struct {
	CheckTarget       `json:",inline" yaml:",inline"`
	LatencyMS         int64      `json:"latency_ms" yaml:"latency_ms"`
	StopReason        string     `json:"stop_reason" yaml:"stop_reason"`
	InputToks         int        `json:"input_tokens" yaml:"input_tokens"`
	OutputToks        int        `json:"output_tokens" yaml:"output_tokens"`
	CachedInputToks   int        `json:"cached_input_tokens" yaml:"cached_input_tokens"`
	CacheCreationToks int        `json:"cache_creation_tokens" yaml:"cache_creation_tokens"`
	CostUSD           float64    `json:"cost_usd" yaml:"cost_usd"`
	Rates             rateCard   `json:"rates" yaml:"rates"`
	RateSource        rateSource `json:"rate_source" yaml:"rate_source"`
	Reply             string     `json:"reply" yaml:"reply"`
}

// The "provider" group itself, and canonProviderID, live in cmd_provider.go —
// this file only hangs a subcommand off it.
var providerCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Send one live completion to a provider and report latency, tokens and cost",
	Long: `Build a provider from the registry (or an OpenAI-compatible preset), send a
single trivial completion, and print the endpoint it dialled, the latency, the
token counts it reported and what paymaster will bill for them.

This is a LOCAL command: the request goes from this machine to the provider, so
it needs no server, token or workspace, and it can reach a local backend the
server cannot.

An API key is required only when the endpoint is the provider's own hosted API.
Pass --base-url and the check runs keyless, which is how a local OpenAI-
compatible backend is reached. Prefer the key's environment variable over
--api-key where you can: an argument is visible to every process on the host
via ps.

Zero tokens reported by a backend that clearly did work is itself the finding:
it means the response carried no usage block, and every call through it will be
billed at $0.

Examples:
  crewship provider check --provider ollama-openai --model qwen2.5:3b
  crewship provider check --provider ollama --model qwen2.5:3b
  crewship provider check --provider anthropic --model claude-haiku-4-5
  crewship provider check --provider openai --base-url http://localhost:11434/v1 --model qwen2.5:3b
  crewship provider check --provider vllm --base-url http://gpu-box:8000/v1 --model Qwen/Qwen3-8B
  crewship provider check --provider deepseek --model deepseek-chat --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var o checkOptions
		o.Provider, _ = cmd.Flags().GetString("provider")
		o.BaseURL, _ = cmd.Flags().GetString("base-url")
		o.Model, _ = cmd.Flags().GetString("model")
		o.APIKey, _ = cmd.Flags().GetString("api-key")
		prompt, _ := cmd.Flags().GetString("prompt")
		timeout, _ := cmd.Flags().GetDuration("timeout")

		prov, target, err := buildCheckProvider(o, os.Getenv)
		if err != nil {
			return err
		}

		// A non-positive deadline would make context.WithTimeout return an
		// ALREADY-expired context, so the call fails before a packet leaves and
		// checkExitCode classifies it as exit 8 — "nothing answered at that
		// address". That is a fabricated connectivity failure: the endpoint was
		// never contacted. `--timeout 0` is a natural spelling of "no limit",
		// so it means that; anything negative is a mistake and is named as one.
		ctx := cmd.Context()
		if timeout < 0 {
			return cli.WithExitCode(
				fmt.Errorf("--timeout must not be negative (got %s); use 0 for no deadline", timeout),
				cli.ExitValidation)
		}
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		res, err := runProviderCheck(ctx, prov, target, prompt)
		if err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(res, func() { printProviderCheck(res) })
	},
}

// buildCheckProvider turns the flags into a constructed provider plus the
// target description printed alongside the result.
//
// getenv is a parameter rather than a direct os.Getenv call so the resolution
// rules — which env var, in what precedence, and when a missing key is fatal —
// can be table-tested without a test mutating the process environment.
//
// Resolution order, matching llm.BuildAuxProviderWithKey: an explicit flag,
// then the spec's env var, then the spec's default. What differs here, and
// deliberately, is that a missing key is fatal only when the endpoint was NOT
// overridden — see the note on the command.
func buildCheckProvider(o checkOptions, getenv func(string) string) (llm.Provider, CheckTarget, error) {
	id := canonProviderID(o.Provider)
	model := strings.TrimSpace(o.Model)
	base := strings.TrimSpace(o.BaseURL)
	if id == "" {
		return nil, CheckTarget{}, cli.WithExitCode(
			fmt.Errorf("--provider is required (%s)", strings.Join(checkProviderNames(), ", ")), cli.ExitValidation)
	}
	if model == "" {
		return nil, CheckTarget{}, cli.WithExitCode(
			fmt.Errorf("--model is required (try 'crewship model list --provider %s')", id), cli.ExitValidation)
	}

	if spec, ok := llm.LookupProvider(id); ok {
		return buildFromSpec(spec, o, base, model, getenv)
	}
	if preset, ok := llm.OpenAIPreset(id); ok {
		return buildFromPreset(id, preset, o, base, model, getenv)
	}
	// NotFoundf, not a bare error: an unknown id is exit 3, and the list of
	// known ones is generated so it cannot become a fifth hardcoded copy.
	return nil, CheckTarget{}, cli.NotFoundf("unknown provider %q (known: %s)", o.Provider, strings.Join(checkProviderNames(), ", "))
}

// buildFromSpec constructs a registry provider. It dispatches on Codec rather
// than calling spec.New, because spec.New is the AUX-SLOT constructor and
// discards the base URL for the two hosted providers — that is right for a slot
// dialling our own API with the server's key, and wrong for a diagnostic whose
// entire job is to prove which endpoint answers.
func buildFromSpec(spec llm.ProviderSpec, o checkOptions, base, model string, getenv func(string) string) (llm.Provider, CheckTarget, error) {
	baseOverridden := base != ""
	if base == "" && spec.BaseEnv != "" {
		base = strings.TrimSpace(getenv(spec.BaseEnv))
	}
	if base == "" {
		base = spec.BaseDefault
	}

	key, keySource := resolveCheckKey(o.APIKey, spec.KeyEnv, getenv)
	if key == "" && spec.KeyEnv != "" && !baseOverridden {
		return nil, CheckTarget{}, cli.WithExitCode(fmt.Errorf(
			"%s is not set (required to reach the %s API — pass --api-key, or --base-url to reach a keyless endpoint)",
			spec.KeyEnv, spec.DisplayName), cli.ExitValidation)
	}

	target := CheckTarget{
		Provider:   spec.ID,
		PricingKey: spec.ID,
		Codec:      string(spec.Codec),
		Endpoint:   redactURL(base),
		Model:      model,
		KeySource:  keySource,
	}

	switch spec.Codec {
	case llm.CodecAnthropicMessages:
		return llm.NewAnthropicWith(llm.AnthropicConfig{
			Name:        spec.ID,
			DisplayName: spec.DisplayName,
			BaseURL:     base,
			APIKey:      key,
		}), target, nil
	case llm.CodecOpenAICompat:
		return llm.NewOpenAICompat(llm.OpenAICompatConfig{
			Name:        spec.ID,
			DisplayName: spec.DisplayName,
			BaseURL:     base,
			APIKey:      key,
			// Non-streaming only, so stream_options never reaches the wire —
			// but declaring it keeps this config identical to the one a
			// streaming caller would build, so a backend that rejects the key
			// fails here rather than later.
			IncludeUsage: true,
		}), target, nil
	case llm.CodecOllamaNative:
		return llm.NewOllama(base, model), target, nil
	}
	// Unreachable while every registered Codec has an arm. A new codec landing
	// without one must say so rather than silently checking the wrong wire.
	return nil, CheckTarget{}, cli.WithExitCode(
		fmt.Errorf("provider %q speaks codec %q, which this command cannot construct", spec.ID, spec.Codec), cli.ExitGeneric)
}

// buildFromPreset constructs one of the shipped OpenAI-compatible backends.
//
// The preset's own Name is kept: it is the pricing key, and ollama-openai
// prices as "ollama" while vllm prices as "local". Overwriting it with the
// flag value the operator typed would silently move both off their free rows.
func buildFromPreset(id string, preset llm.OpenAICompatConfig, o checkOptions, base, model string, getenv func(string) string) (llm.Provider, CheckTarget, error) {
	if base != "" {
		preset.BaseURL = base
	}
	if preset.BaseURL == "" {
		// vllm ships no base URL because there is no default vLLM address.
		// Leaving it empty would let withDefaults resolve it to
		// api.openai.com and send the operator's traffic — with whatever key
		// they passed — somewhere they did not ask for.
		return nil, CheckTarget{}, cli.WithExitCode(
			fmt.Errorf("--base-url is required for the %q preset (it ships no default endpoint)", id), cli.ExitValidation)
	}

	// A preset has no KeyEnv of its own; it inherits one only when its pricing
	// name is also a registry id (the "openai" preset → OPENAI_API_KEY). No env
	// convention is invented for the rest — an unauthenticated call to a
	// provider that wants a key returns the upstream 401, which names the
	// problem better than a guess at a variable name would.
	keyEnv := ""
	if spec, ok := llm.LookupProvider(preset.Name); ok {
		keyEnv = spec.KeyEnv
	}
	key, keySource := resolveCheckKey(o.APIKey, keyEnv, getenv)
	preset.APIKey = key
	// The keyless presets (vllm, ollama-openai) carry NoAuth, and applyHeaders
	// returns early on it — so assigning APIKey alone would drop the key on the
	// floor and send no Authorization header at all. A vLLM started with
	// --api-key then answers 401 while this command reports `api key --api-key`,
	// pointing the operator at the key's value instead of at the header that was
	// never sent. Supplying a key is an unambiguous request to authenticate.
	if key != "" {
		preset.NoAuth = false
	}

	return llm.NewOpenAICompat(preset), CheckTarget{
		Provider:   id,
		PricingKey: preset.Name,
		Codec:      string(llm.CodecOpenAICompat),
		Endpoint:   redactURL(preset.BaseURL),
		Model:      model,
		KeySource:  keySource,
	}, nil
}

// resolveCheckKey returns the API key and a label for where it came from. The
// label never contains the key.
func resolveCheckKey(flagKey, keyEnv string, getenv func(string) string) (string, string) {
	if k := strings.TrimSpace(flagKey); k != "" {
		return k, "--api-key"
	}
	if keyEnv == "" {
		return "", "none"
	}
	if k := strings.TrimSpace(getenv(keyEnv)); k != "" {
		return k, "$" + keyEnv
	}
	return "", "unset ($" + keyEnv + ")"
}

// checkProviderNames lists everything --provider accepts: the registry ids in
// declaration order, then the openai-compat presets that are not already one.
func checkProviderNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, id := range llm.RegisteredProviders() {
		add(id)
	}
	for _, name := range llm.OpenAIPresetNames() {
		add(name)
	}
	return out
}

// runProviderCheck performs the one call and assembles the report. The provider
// arrives already constructed so the tests can drive a real codec against an
// httptest server — the command's own network call is the only thing that is
// not exercised, and it is the one line that cannot be.
func runProviderCheck(ctx context.Context, p llm.Provider, target CheckTarget, prompt string) (providerCheckResult, error) {
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultCheckPrompt
	}
	start := time.Now()
	resp, err := p.Complete(ctx, llm.Request{
		Model:     target.Model,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: prompt}},
		MaxTokens: checkMaxTokens,
	})
	elapsed := time.Since(start)
	if err != nil {
		// The upstream text is preserved verbatim — a provider's own "model
		// not found" or "invalid api key" is the diagnosis, and rewording it
		// would throw away the only part of the message that is specific.
		return providerCheckResult{}, cli.WithExitCode(
			fmt.Errorf("%s check failed after %s: %w", target.Provider, elapsed.Round(time.Millisecond), err),
			checkExitCode(err))
	}

	ex := explainRate(target.PricingKey, target.Model)
	return providerCheckResult{
		CheckTarget:       target,
		LatencyMS:         elapsed.Milliseconds(),
		StopReason:        string(resp.StopReason),
		InputToks:         resp.InputToks,
		OutputToks:        resp.OutputToks,
		CachedInputToks:   resp.CachedInputToks,
		CacheCreationToks: resp.CacheCreationToks,
		CostUSD: paymasterEstimateForCheck(target.PricingKey, target.Model,
			resp.InputToks, resp.OutputToks, resp.CachedInputToks, resp.CacheCreationToks),
		Rates:      ex.Rates,
		RateSource: ex.Source,
		Reply:      resp.Content,
	}, nil
}

// paymasterEstimateForCheck widens the codec's int counters to the int64
// paymaster prices in. It is a named function rather than four inline casts so
// the conversion has somewhere to be read: llm.Response counts are int and the
// ledger's are int64, and this is the boundary between them.
func paymasterEstimateForCheck(provider, model string, in, out, cachedIn, cacheCreate int) float64 {
	return paymaster.Estimate(provider, model, int64(in), int64(out), int64(cachedIn), int64(cacheCreate))
}

// checkExitCode classifies a failed completion. A transport failure — DNS,
// refused connection, TLS, timeout — arrives wrapped in a *url.Error from
// http.Client.Do, while an API rejection is a plain formatted string carrying
// the status. That distinction is worth keeping in the exit code: 8 means
// "nothing answered at that address", 7 means "the provider answered and said
// no", and a script retrying the first but not the second is right to.
//
// *net.OpError is checked as well as *url.Error even though the codecs always
// produce the latter: a codec that dials without going through http.Client —
// or a future one that speaks a non-HTTP transport — would otherwise report a
// refused connection as a provider rejection.
//
// A bare context error is the third shape, and it is why the two errors.As
// calls are not enough on their own: llm/httpretry.go returns ctx.Err()
// unwrapped when the deadline expires during retry backoff, so a connection
// that was refused and then retried into the deadline carries no *url.Error at
// all. Since the deadline is the one this command set (--timeout), its expiry
// means nothing answered inside the window — the same class as a refused
// connection, not a provider verdict.
func checkExitCode(err error) int {
	var ue *url.Error
	var oe *net.OpError
	switch {
	case errors.As(err, &ue), errors.As(err, &oe):
		return cli.ExitConnection
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return cli.ExitConnection
	}
	return cli.ExitServer
}

func printProviderCheck(res providerCheckResult) {
	fmt.Printf("%s%s%s  %s\n", cli.Bold, res.Provider, cli.Reset, res.Model)
	fmt.Printf("  codec        %s\n", res.Codec)
	fmt.Printf("  endpoint     %s\n", res.Endpoint)
	fmt.Printf("  api key      %s\n", res.KeySource)
	if res.PricingKey != res.Provider {
		fmt.Printf("  pricing key  %s %s(what the ledger bills this as)%s\n", res.PricingKey, cli.Dim, cli.Reset)
	}
	fmt.Println()
	fmt.Printf("  latency      %s\n", time.Duration(res.LatencyMS)*time.Millisecond)
	fmt.Printf("  stop reason  %s\n", res.StopReason)
	fmt.Printf("  tokens       in %d  out %d  cached-in %d  cache-write %d\n",
		res.InputToks, res.OutputToks, res.CachedInputToks, res.CacheCreationToks)
	fmt.Printf("  cost         %s%s%s %s(%s rates: %s)%s\n",
		cli.Bold, formatCostUSD(res.CostUSD), cli.Reset,
		cli.Dim, res.RateSource, formatCardLine(res.Rates), cli.Reset)
	if res.InputToks == 0 && res.OutputToks == 0 {
		fmt.Printf("  %swarning      the response carried no usage block — every call through this backend bills at $0%s\n", cli.Dim, cli.Reset)
	}
	fmt.Println()
	fmt.Printf("  reply        %s\n", strings.TrimSpace(res.Reply))
}

func init() {
	providerCheckCmd.Flags().String("provider", "", "Provider id or openai-compat preset to dial")
	providerCheckCmd.Flags().String("base-url", "", "Override the endpoint (required for presets that ship none)")
	providerCheckCmd.Flags().String("model", "", "Model id to send the completion to")
	providerCheckCmd.Flags().String("api-key", "", "API key; defaults to the provider's key environment variable")
	providerCheckCmd.Flags().String("prompt", defaultCheckPrompt, "Prompt to send")
	providerCheckCmd.Flags().Duration("timeout", defaultCheckTimeout, "Deadline for the whole call")
	providerCmd.AddCommand(providerCheckCmd)
}
