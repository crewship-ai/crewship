package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/llm/endpoint"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

// AdminKeeperJudgeHandler is the two things that turn "paste a URL and hope"
// into a setup you can finish in a minute:
//
//	GET  /api/v1/admin/keeper/judge/models  — what this endpoint actually serves
//	POST /api/v1/admin/keeper/judge/test    — does the judge work, in three stages
//
// Why three stages rather than one green tick. Keeper is fail-closed, so every
// way of being broken arrives as the same DENY on every credential request. The
// three stages separate the three causes an operator can act on, and they are
// genuinely different problems:
//
//  1. Reach     — is anything answering on that address? (wrong host, wrong port,
//     Ollama bound to loopback on another machine, firewall)
//  2. Model     — is the model pulled, and can it chat? (`ollama pull` not run,
//     or an embedding-only model that will never classify)
//  3. Verdict   — does it return a parseable verdict? (a 0.5B model passes 1 and
//     2 and still cannot produce JSON; a chatty model DENYs everything)
//
// Stage 3 is the one that matters and the one a plain ping cannot give you: it
// runs the real llm.Ollama provider on a miniature gatekeeper prompt, so what it
// proves is that the code path the judge uses works — not that a port is open.
//
// Both routes are OWNER/ADMIN: they dial an address the caller can supply, which
// is a write-class capability even though the response is a model list.
type AdminKeeperJudgeHandler struct {
	store  *keepercfg.Store
	logger *slog.Logger
	// gov builds a candidate hosted judge (Anthropic / OpenAI-compatible) from a
	// workspace's vault so TestHosted can answer "does the key I picked work"
	// before it is saved. nil on a router without the resolver wired — the route
	// then reports 503 rather than pretending.
	gov *GovModelResolver

	// probes bounds how often either route may dial. Both take an address from
	// the caller, so this is the difference between a configuration tool and a
	// network scanner with an admin login. Instance-wide (not per IP): the
	// capability being rationed is the daemon's outbound dial, not a client's
	// share of it. The rate is read from the ratelimitcfg registry on every call
	// so an operator override applies without a restart.
	//
	// The bucket itself lives in keeper_spend_limiter.go, shared with the manual
	// Reviews trigger (#1575) — the other admin route that spends a model call
	// because somebody pressed something. Burst 0 = the configured value, which
	// is the shape this route has always had (6/min, burst 6).
	probes *spendLimiter
}

func NewAdminKeeperJudgeHandler(store *keepercfg.Store, logger *slog.Logger) *AdminKeeperJudgeHandler {
	return &AdminKeeperJudgeHandler{
		store:  store,
		logger: logger,
		probes: newSpendLimiter(ratelimitcfg.KeyKeeperJudgeProbe, time.Minute, 0),
	}
}

// allowProbe consumes one token, retuning the bucket from the registry first so
// a live override is honoured. Returns false when the caller should get a 429.
func (h *AdminKeeperJudgeHandler) allowProbe() bool {
	ok, _ := h.probes.take()
	return ok
}

// judgeProbeTimeout bounds one stage. Generous enough for a cold model load on a
// CPU-only box (the first /api/chat after a quiet hour pays for the weights),
// tight enough that a wedged endpoint does not hold an admin request open.
const judgeProbeTimeout = 60 * time.Second

// judgeRoot reduces whatever an operator pasted to the root the native Ollama
// API hangs off, via the canonical normalizer (#1528). Every paste shape —
// `:11434`, a trailing slash, `/v1`, `/v1/chat/completions`, `/api/chat` — lands
// on the same Root, and a reverse-proxy mount prefix is preserved.
//
// This is what makes "test green, production DENY" unreachable through the
// endpoint field: the URL this checks and the URL the judge dials are derived by
// the same function.
//
// It also refuses a container-only hostname up front. That value is not a typo —
// host.docker.internal is CORRECT for the agent path, which shares this
// credential — but the judge dials from the daemon, where it resolves to nothing.
// Saying so beats waiting out a DNS timeout and reporting "unreachable".
func judgeRoot(raw string) (string, error) {
	ep, err := endpoint.Normalize(raw)
	if err != nil {
		if strings.TrimSpace(raw) == "" {
			return "", errNoJudgeEndpoint
		}
		return "", &judgeEndpointError{err.Error()}
	}
	if ep.IsContainerOnlyHost() {
		return "", &judgeEndpointError{
			ep.Root.Hostname() + " only resolves inside containers, and the judge dials from the host — use localhost or the machine's LAN address"}
	}
	return ep.String(), nil
}

type judgeEndpointError struct{ msg string }

func (e *judgeEndpointError) Error() string { return e.msg }

var errNoJudgeEndpoint = &judgeEndpointError{"no judge endpoint is configured — set one first"}

// WithGovJudge attaches the concrete governance-model resolver used by
// TestHosted. Nil-safe: an unwired resolver makes that one route report 503 and
// leaves the local check working, which is what a router without the governance
// wiring (tests, embedded builds) should do.
func (h *AdminKeeperJudgeHandler) WithGovJudge(g *GovModelResolver) *AdminKeeperJudgeHandler {
	h.gov = g
	return h
}

// judgeStage is one step of the check.
type judgeStage struct {
	Name      string `json:"name"`  // reach | model | verdict
	Label     string `json:"label"` // human-facing step name
	OK        bool   `json:"ok"`
	Skipped   bool   `json:"skipped,omitempty"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

type judgeTestResponse struct {
	OK       bool         `json:"ok"`
	Endpoint string       `json:"endpoint"`
	Model    string       `json:"model,omitempty"`
	Stages   []judgeStage `json:"stages"`
	// Models is what the endpoint advertised, so a failed stage 2 can offer the
	// right answer instead of only reporting the wrong one.
	Models []string `json:"models,omitempty"`
	// Decision is the verdict the smoke prompt produced, when stage 3 ran.
	Decision string `json:"decision,omitempty"`
}

type judgeTestRequest struct {
	// Optional: test values the operator has typed but not saved. Empty falls
	// back to what is in force, so the button works before and after a save.
	EndpointURL string `json:"judge_endpoint_url"`
	Model       string `json:"judge_model"`
}

// Test runs the three-stage check. POST /api/v1/admin/keeper/judge/test
func (h *AdminKeeperJudgeHandler) Test(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !h.allowProbe() {
		replyError(w, http.StatusTooManyRequests,
			"Too many judge probes — these dial an address you supply, so they are rate limited instance-wide. Try again in a moment.")
		return
	}
	var body judgeTestRequest
	// An empty body is the common case (test what is saved), so a decode failure
	// on no content must not be an error.
	if r.ContentLength > 0 {
		if err := readJSON(r, &body); err != nil {
			replyError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}
	}

	endpoint, model := strings.TrimSpace(body.EndpointURL), strings.TrimSpace(body.Model)
	if h.store != nil {
		eff := h.store.Effective()
		if endpoint == "" {
			endpoint = eff.EndpointURL.Value
		}
		if model == "" {
			model = eff.Model.Value
		}
	}

	root, err := judgeRoot(endpoint)
	if err != nil {
		writeJSON(w, http.StatusOK, judgeTestResponse{
			Endpoint: endpoint,
			Model:    model,
			Stages: []judgeStage{{
				Name: "reach", Label: "Reach the endpoint", OK: false, Detail: err.Error(),
			}},
		})
		return
	}

	resp := h.run(r.Context(), root, model)
	h.logger.Info("keeper: judge test run",
		"endpoint", root, "model", model, "ok", resp.OK)
	writeJSON(w, http.StatusOK, resp)
}

// run executes the three stages against root, stopping when a stage makes the
// next one meaningless.
func (h *AdminKeeperJudgeHandler) run(ctx context.Context, root, model string) judgeTestResponse {
	out := judgeTestResponse{Endpoint: root, Model: model}
	client := httpsafe.TrustedEndpointClient(judgeProbeTimeout)
	provider := llm.NewOllamaWithClient(root, model, client)

	// ── Stage 1: reach ──
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	started := time.Now()
	models, err := provider.ListModels(listCtx)
	cancel()
	reach := judgeStage{Name: "reach", Label: "Reach the endpoint", LatencyMS: time.Since(started).Milliseconds()}
	if err != nil {
		reach.Detail = judgeReachHint(root, err)
		out.Stages = append(out.Stages, reach,
			skipped("model", "Model is available", "not checked — the endpoint did not answer"),
			skipped("verdict", "Returns a verdict", "not checked — the endpoint did not answer"))
		return out
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.ID)
	}
	out.Models = names
	reach.OK = true
	reach.Detail = pluralModels(len(names))
	out.Stages = append(out.Stages, reach)

	// ── Stage 2: the model is there ──
	stage2 := judgeStage{Name: "model", Label: "Model is available"}
	switch {
	case model == "":
		stage2.Detail = "no model is configured — pick one from the list"
	case containsModel(names, model):
		stage2.OK = true
		stage2.Detail = model + " is pulled and ready"
	default:
		stage2.Detail = "the endpoint is up, but " + model + " is not pulled there — run `ollama pull " + model + "` or pick one of the models it already has"
	}
	out.Stages = append(out.Stages, stage2)
	if !stage2.OK {
		out.Stages = append(out.Stages,
			skipped("verdict", "Returns a verdict", "not checked — no usable model"))
		return out
	}

	// ── Stage 3: it returns a verdict ──
	// The real provider on a miniature gatekeeper prompt. A model that answers in
	// prose DENYs every credential request in production, and a model too small to
	// follow the format passes stages 1 and 2 without ever being able to judge.
	chatCtx, cancelChat := context.WithTimeout(ctx, judgeProbeTimeout)
	defer cancelChat()
	zero := 0.0
	started = time.Now()
	answer, err := provider.Complete(chatCtx, llm.Request{
		Model:     model,
		System:    "You are a security gatekeeper. Reply with ONLY compact JSON: {\"decision\":\"ALLOW|DENY|ESCALATE\",\"reason\":\"...\",\"risk\":1-10}. No prose, no code fence.",
		Messages:  []llm.Message{{Role: "user", Content: "Agent 'ci-bot' asks for credential 'npm-publish-token' (level L1). Intent: \"publish the release tarball to npm as part of the tagged build\". Decide."}},
		MaxTokens: 200,
		// A security verdict must be reproducible; an audit trail of sampled
		// decisions is not defensible.
		Temperature: &zero,
	})
	stage3 := judgeStage{Name: "verdict", Label: "Returns a verdict", LatencyMS: time.Since(started).Milliseconds()}
	switch {
	case err != nil:
		stage3.Detail = "the model did not answer: " + err.Error()
	default:
		decision, ok := parseSmokeVerdict(answer.Content)
		if !ok {
			stage3.Detail = "the model answered, but not with a verdict — Keeper is fail-closed, so this model would deny every request. Try a larger instruct model."
			break
		}
		stage3.OK = true
		out.Decision = decision
		stage3.Detail = "verdict: " + decision
	}
	out.Stages = append(out.Stages, stage3)
	out.OK = stage3.OK

	// ── Stage 4: it answers inside the budget the credential path allows ──
	//
	// The stage this check was missing, and the reason it lied. Stages 1–3 measure
	// with the PROBE's timeout, which is generous on purpose. The credential path
	// uses the operator's judge budget, and a judge slower than that budget DENIES
	// every request — fail-closed. On dev1 a correctly configured 7B judge took
	// ~12s against a 5s budget: three green ticks, "the judge works", and every
	// credential request refused with what looked like a security verdict.
	//
	// So the last stage compares what we just measured against what production
	// will allow, and it is not cosmetic: OK is ANDed with it, because a judge that
	// cannot answer in time does not work, however well it reasons.
	if stage3.OK {
		stage4 := h.budgetStage(stage3.LatencyMS)
		out.Stages = append(out.Stages, stage4)
		out.OK = out.OK && stage4.OK
	}
	return out
}

// suggestBudget rounds a measured latency up to a round number with headroom, for
// the copy-pasteable command in the stage-4 failure. Doubling rather than adding a
// few seconds: the measurement is one warm call, and the first request after an
// idle period pays for a cold model load.
func suggestBudget(took time.Duration) time.Duration {
	want := took * 2
	if want < 10*time.Second {
		want = 10 * time.Second
	}
	if want > keepercfg.MaxJudgeTimeout {
		want = keepercfg.MaxJudgeTimeout
	}
	return want.Round(5 * time.Second)
}

func skipped(name, label, detail string) judgeStage {
	return judgeStage{Name: name, Label: label, Skipped: true, Detail: detail}
}

func pluralModels(n int) string {
	switch n {
	case 0:
		return "answering, but it has no models pulled yet — run `ollama pull qwen2.5:7b`"
	case 1:
		return "answering · 1 model available"
	default:
		return "answering · " + strconv.Itoa(n) + " models available"
	}
}

// containsModel matches a model name, tolerating the omitted ":latest" tag the
// way Ollama itself does — a list entry "qwen2.5:latest" satisfies "qwen2.5".
func containsModel(names []string, want string) bool {
	for _, n := range names {
		if n == want || n == want+":latest" || strings.TrimSuffix(n, ":latest") == want {
			return true
		}
	}
	return false
}

// judgeReachHint turns a transport error into the sentence that fixes it. The
// container-hostname case is worth its own message: the endpoint is shared with
// the agent path, where host.docker.internal is CORRECT, and the judge dials
// from the host where it is not.
func judgeReachHint(root string, err error) string {
	msg := err.Error()
	if strings.Contains(root, "host.docker.internal") {
		return "host.docker.internal only resolves inside containers, and the judge dials from the host — use localhost or the machine's LAN address (" + msg + ")"
	}
	switch {
	case strings.Contains(msg, "connection refused"):
		return "nothing is listening there. If Ollama runs on another machine, it must be bound to that machine's address (OLLAMA_HOST), not just loopback (" + msg + ")"
	case strings.Contains(msg, "blocked address"):
		return "that address is blocked for security (cloud metadata / link-local). Private and loopback addresses are allowed (" + msg + ")"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "the endpoint did not answer in time — check the address and any firewall between here and it"
	default:
		return "could not reach the endpoint: " + msg
	}
}

// parseSmokeVerdict extracts a decision from a model answer using the same
// brace-scan the gatekeeper's own parser uses, so a model that passes here
// passes there.
func parseSmokeVerdict(content string) (string, bool) {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return "", false
	}
	var v struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &v); err != nil {
		return "", false
	}
	switch strings.ToUpper(strings.TrimSpace(v.Decision)) {
	case "ALLOW":
		return "ALLOW", true
	case "DENY":
		return "DENY", true
	case "ESCALATE":
		return "ESCALATE", true
	default:
		return "", false
	}
}

// ── Model discovery ─────────────────────────────────────────────────────────

type judgeModelsResponse struct {
	Endpoint string   `json:"endpoint"`
	Models   []string `json:"models"`
	// Suggestions are candidate addresses to try — see judgeSuggestions. Sent
	// with every answer, not just failures: the useful moment to learn that your
	// own laptop is reachable is before you have typed anything.
	Suggestions []judgeSuggestion `json:"suggestions,omitempty"`
	// Error is set instead of an HTTP error status: the picker asks on every
	// keystroke-settled edit, and a 500 in the console for "your Ollama is not
	// running yet" is noise. The UI renders this next to the field.
	Error string `json:"error,omitempty"`
}

// Models lists what the endpoint serves, so the model field can be a picker
// instead of a free-text field an operator can typo into a fail-closed DENY.
// GET /api/v1/admin/keeper/judge/models[?endpoint=…]
func (h *AdminKeeperJudgeHandler) Models(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if !h.allowProbe() {
		replyError(w, http.StatusTooManyRequests,
			"Too many judge probes — these dial an address you supply, so they are rate limited instance-wide. Try again in a moment.")
		return
	}
	endpointArg := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	if endpointArg == "" && h.store != nil {
		endpointArg = h.store.Effective().EndpointURL.Value
	}
	root, err := judgeRoot(endpointArg)
	if err != nil {
		writeJSON(w, http.StatusOK, judgeModelsResponse{
			Endpoint: endpointArg, Error: err.Error(), Suggestions: judgeSuggestions(r)})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	models, err := llm.NewOllamaWithClient(root, "", httpsafe.TrustedEndpointClient(15*time.Second)).ListModels(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, judgeModelsResponse{
			Endpoint: root, Error: judgeReachHint(root, err), Suggestions: judgeSuggestions(r)})
		return
	}
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.ID)
	}
	writeJSON(w, http.StatusOK, judgeModelsResponse{
		Endpoint: root, Models: names, Suggestions: judgeSuggestions(r)})
}

// hostedJudgeTestRequest is a not-yet-saved hosted judge configuration.
type hostedJudgeTestRequest struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	CredentialID string `json:"credential_id"`
}

// TestHosted probes a hosted judge (Anthropic or OpenAI-compatible) built from a
// workspace's vault key, before the configuration is saved.
// POST /api/v1/admin/keeper/judge/test-hosted
//
// The gap this closes: the local Ollama judge had a four-stage check and a hosted
// one had none. An operator picking "Anthropic" and one of several stored API keys
// — which is the normal case on an orchestration platform, where each key carries
// its own subscription limit — got no feedback until the next real credential
// request, and if they picked the exhausted or wrong-typed key that feedback
// arrived as a fail-closed DENY. "Which key is this and does it work" is the
// question the page asks the operator to answer; it should be able to answer it
// back.
//
// Stages differ from the local check because the failure modes differ: there is no
// endpoint to reach and no model to pull, but there IS a key that may be missing,
// revoked, or of the wrong type — which is the stage a local judge does not have.
func (h *AdminKeeperJudgeHandler) TestHosted(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if h.gov == nil {
		replyError(w, http.StatusServiceUnavailable, "Governance-model resolution is not available on this server")
		return
	}
	// Shares the instance-wide probe bucket with the local check: both spend the
	// daemon's outbound dial on a caller-supplied target, and a hosted probe also
	// spends the operator's money.
	if !h.allowProbe() {
		replyError(w, http.StatusTooManyRequests,
			"Too many judge probes — these call a model on an address you supply, so they are rate limited instance-wide. Try again in a moment.")
		return
	}

	var body hostedJudgeTestRequest
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(body.Provider))
	model := strings.TrimSpace(body.Model)
	if provider == "" {
		replyError(w, http.StatusBadRequest, "Pick a provider to test")
		return
	}
	if model == "" {
		replyError(w, http.StatusBadRequest, "Pick a model to test — a provider alone has nothing to ask")
		return
	}

	out := judgeTestResponse{Model: model}
	workspaceID := WorkspaceIDFromContext(r.Context())

	// ── Stage 1: the credential resolves to a usable key ──
	started := time.Now()
	prov, resolved, err := h.gov.BuildCandidate(r.Context(), workspaceID, provider, model, strings.TrimSpace(body.CredentialID))
	stage1 := judgeStage{Name: "key", Label: "Key resolves", LatencyMS: time.Since(started).Milliseconds()}
	switch {
	case err != nil:
		stage1.Detail = err.Error()
		out.Stages = append(out.Stages, stage1,
			skipped("verdict", "Returns a verdict", "not checked — no usable provider"))
		writeJSON(w, http.StatusOK, out)
		return
	case resolved.Degraded:
		// A degraded candidate is the §4.4 contract working, not a crash: the
		// judge would still run, on the LOCAL model rather than the one being
		// tested. Reporting it as a pass would tell the operator their Anthropic
		// key works when it is not being used at all.
		stage1.Detail = "this configuration would not be used — " + resolved.DegradeReason
		out.Stages = append(out.Stages, stage1,
			skipped("verdict", "Returns a verdict", "not checked — the configuration degrades to the local judge"))
		writeJSON(w, http.StatusOK, out)
		return
	default:
		stage1.OK = true
		if strings.TrimSpace(body.CredentialID) == "" {
			stage1.Detail = "no vault credential selected — falling back to the server's environment key"
		} else {
			stage1.Detail = "the selected credential decrypts and is the right type"
		}
	}
	out.Stages = append(out.Stages, stage1)

	// ── Stage 2: it returns a verdict ──
	// Same miniature gatekeeper prompt as the local check, so "works" means the
	// same thing for both kinds of judge.
	chatCtx, cancel := context.WithTimeout(r.Context(), judgeProbeTimeout)
	defer cancel()
	zero := 0.0
	started = time.Now()
	answer, cerr := prov.Complete(chatCtx, llm.Request{
		Model:     model,
		System:    "You are a security gatekeeper. Reply with ONLY compact JSON: {\"decision\":\"ALLOW|DENY|ESCALATE\",\"reason\":\"...\",\"risk\":1-10}. No prose, no code fence.",
		Messages:  []llm.Message{{Role: "user", Content: "Agent 'ci-bot' asks for credential 'npm-publish-token' (level L1). Intent: \"publish the release tarball to npm as part of the tagged build\". Decide."}},
		MaxTokens: 200,
		// A security verdict must be reproducible; an audit trail of sampled
		// decisions is not defensible.
		Temperature: &zero,
	})
	stage2 := judgeStage{Name: "verdict", Label: "Returns a verdict", LatencyMS: time.Since(started).Milliseconds()}
	switch {
	case cerr != nil:
		// The most valuable line in this whole endpoint: a 401 means the wrong
		// key, a 429 means the key is rate-limited or its subscription is spent.
		// Both are things the operator fixes by picking a different stored key,
		// and both were previously invisible until a credential request denied.
		stage2.Detail = "the model did not answer: " + cerr.Error()
	default:
		decision, ok := parseSmokeVerdict(answer.Content)
		if !ok {
			stage2.Detail = "the model answered, but not with a verdict — Keeper is fail-closed, so this model would deny every request."
			break
		}
		stage2.OK = true
		out.Decision = decision
		stage2.Detail = "verdict: " + decision
	}
	out.Stages = append(out.Stages, stage2)
	out.OK = stage1.OK && stage2.OK

	// ── Stage 3: inside the credential path's budget ──
	if stage2.OK {
		out.Stages = append(out.Stages, h.budgetStage(stage2.LatencyMS))
		out.OK = out.OK && out.Stages[len(out.Stages)-1].OK
	}

	h.logger.Info("keeper: hosted judge test run",
		"provider", provider, "model", model, "credential_selected", body.CredentialID != "", "ok", out.OK)
	writeJSON(w, http.StatusOK, out)
}

// budgetStage compares a measured verdict latency against the budget the
// credential path will actually allow. Shared by both checks so a local and a
// hosted judge are held to the same bar — the bar being "fast enough that Keeper
// does not fail closed on it", which is the only definition that matters.
func (h *AdminKeeperJudgeHandler) budgetStage(latencyMS int64) judgeStage {
	budget := keepercfg.DefaultJudgeTimeout
	if h.store != nil {
		if ms := h.store.Effective().TimeoutMS.Value; ms > 0 {
			budget = time.Duration(ms) * time.Millisecond
		}
	}
	took := time.Duration(latencyMS) * time.Millisecond
	st := judgeStage{Name: "budget", Label: "Answers inside the budget", LatencyMS: latencyMS}
	switch {
	case took <= budget/2:
		st.OK = true
		st.Detail = fmt.Sprintf("%s of a %s budget — comfortable headroom", took.Round(time.Millisecond), budget)
	case took <= budget:
		st.OK = true
		st.Detail = fmt.Sprintf(
			"%s of a %s budget — it fits, but only just. A cold model load is slower than this, so consider raising the budget.",
			took.Round(time.Millisecond), budget)
	default:
		st.Detail = fmt.Sprintf(
			"%s, and the budget is %s — this judge would DENY every credential request. Raise the budget (`crewship keeper config set --judge-timeout %s`) or pick a faster model.",
			took.Round(time.Millisecond), budget, suggestBudget(took))
	}
	return st
}

// judgeSuggestion is a candidate model-server address the console can offer as a
// one-click fill.
type judgeSuggestion struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// judgeSuggestions proposes where an Ollama might be, so "point it at a model
// server" is a choice rather than a guess.
//
// The question this answers is the one that has now been asked out loud: "how can
// it be localhost when the server runs on Proxmox and my Ollama runs on my Mac?"
// It cannot — and the operator has no way to know the address to type, because
// the machine that dials is not the machine they are sitting at. The daemon
// already knows both: its own loopback, and the address the browser connected
// FROM, which on a LAN is exactly the laptop running Ollama.
//
// Trust: a suggestion is never dialled on its own — the operator clicks it, then
// clicks Connect, which goes through the SSRF fence and the probe rate limit like
// any other address. RemoteAddr is used directly; an X-Forwarded-For hop is only
// taken when it parses as a PRIVATE address, because that is both the only case
// where the suggestion could be right and the only case where a spoofed header
// buys nothing (the fence permits private ranges regardless).
func judgeSuggestions(r *http.Request) []judgeSuggestion {
	out := []judgeSuggestion{
		{URL: "http://localhost:11434", Label: "Ollama on the Crewship server itself"},
	}

	client := clientAddrForSuggestion(r)
	if client != "" && client != "127.0.0.1" && client != "::1" {
		host := client
		if strings.Contains(host, ":") { // IPv6 literal
			host = "[" + host + "]"
		}
		out = append(out, judgeSuggestion{
			URL:   "http://" + host + ":11434",
			Label: "Ollama on the machine you are browsing from",
		})
	}
	return out
}

// clientAddrForSuggestion returns the browser's apparent address, preferring a
// private X-Forwarded-For hop when the direct peer is a loopback proxy.
func clientAddrForSuggestion(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(strings.TrimSpace(host))

	// Behind a same-box reverse proxy the peer is loopback and tells us nothing;
	// the forwarded hop is the only place the real LAN address appears. Only a
	// private one is taken — see the trust note on judgeSuggestions.
	if peer == nil || peer.IsLoopback() {
		for _, h := range r.Header.Values("X-Forwarded-For") {
			for _, p := range strings.Split(h, ",") {
				if ip := net.ParseIP(strings.TrimSpace(p)); ip != nil && ip.IsPrivate() {
					return ip.String()
				}
			}
		}
	}
	if peer == nil {
		return ""
	}
	return peer.String()
}

// ProbeModel runs the verdict + budget stages against an arbitrary
// provider/model, without saving anything. It is the shared implementation
// behind the per-evaluator probe on the Judge models card.
//
// Exported as a method value rather than a route: the aux handler owns "which
// slot", this owns "does a model answer, and fast enough" — and having one
// implementation of the second is what keeps a local and a hosted evaluator held
// to the same bar.
func (h *AdminKeeperJudgeHandler) ProbeModel(w http.ResponseWriter, r *http.Request, provider, model string) {
	if !h.allowProbe() {
		replyError(w, http.StatusTooManyRequests,
			"Too many judge probes — each one calls a model, so they are rate limited instance-wide. Try again in a moment.")
		return
	}

	out := judgeTestResponse{Model: model}
	var prov llm.Provider

	switch strings.ToLower(strings.TrimSpace(provider)) {
	case keepercfg.ProviderOllama:
		// The instance judge's endpoint, because that is what an "ollama"
		// evaluator slot resolves to at request time (keeper_aux_live.go).
		endpoint := ""
		if h.store != nil {
			endpoint = h.store.Effective().EndpointURL.Value
		}
		root, err := judgeRoot(endpoint)
		if err != nil {
			writeJSON(w, http.StatusOK, judgeTestResponse{Model: model, Stages: []judgeStage{{
				Name: "key", Label: "Provider resolves", Detail: "no usable judge endpoint: " + err.Error(),
			}}})
			return
		}
		out.Endpoint = root
		prov = llm.NewOllamaWithClient(root, model, httpsafe.TrustedEndpointClient(judgeProbeTimeout))
	default:
		if h.gov == nil {
			replyError(w, http.StatusServiceUnavailable, "Hosted-provider probing is not available on this server")
			return
		}
		p, resolved, err := h.gov.BuildCandidate(r.Context(), WorkspaceIDFromContext(r.Context()), provider, model, "")
		if err != nil || resolved.Degraded {
			// The degrade is the §4.4 contract working, not a crash — but reporting
			// it as anything other than a failure would tell the operator this
			// evaluator answers when the local judge is what would actually run.
			detail := "this configuration would not be used — " + resolved.DegradeReason
			if err != nil {
				detail = err.Error()
			}
			writeJSON(w, http.StatusOK, judgeTestResponse{Model: model, Stages: []judgeStage{{
				Name: "key", Label: "Provider resolves", Detail: detail,
			}}})
			return
		}
		prov = p
	}

	ctx, cancel := context.WithTimeout(r.Context(), judgeProbeTimeout)
	defer cancel()
	zero := 0.0
	started := time.Now()
	answer, err := prov.Complete(ctx, llm.Request{
		Model:     model,
		System:    "You are a security gatekeeper. Reply with ONLY compact JSON: {\"decision\":\"ALLOW|DENY|ESCALATE\",\"reason\":\"...\",\"risk\":1-10}. No prose, no code fence.",
		Messages:  []llm.Message{{Role: "user", Content: "Agent 'ci-bot' asks for credential 'npm-publish-token' (level L1). Intent: \"publish the release tarball to npm as part of the tagged build\". Decide."}},
		MaxTokens: 200,
		// A security verdict must be reproducible; an audit trail of sampled
		// decisions is not defensible.
		Temperature: &zero,
	})
	st := judgeStage{Name: "verdict", Label: "Returns a verdict", LatencyMS: time.Since(started).Milliseconds()}
	switch {
	case err != nil:
		st.Detail = "the model did not answer: " + err.Error()
	default:
		decision, ok := parseSmokeVerdict(answer.Content)
		if !ok {
			st.Detail = "the model answered, but not with a verdict — this evaluator would fail closed on every run."
			break
		}
		st.OK = true
		out.Decision = decision
		st.Detail = "verdict: " + decision
	}
	out.Stages = append(out.Stages, st)
	out.OK = st.OK
	if st.OK {
		budget := h.budgetStage(st.LatencyMS)
		out.Stages = append(out.Stages, budget)
		out.OK = out.OK && budget.OK
	}

	h.logger.Info("keeper: evaluator probe", "provider", provider, "model", model, "ok", out.OK)
	writeJSON(w, http.StatusOK, out)
}
