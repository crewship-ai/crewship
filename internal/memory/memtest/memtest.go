package memtest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/memory"
)

// ─── Fixture builders ────────────────────────────────────────────────────

// Default values used by every fixture builder. Lifted to constants so
// future tests can compare against them (e.g. "did the dispatcher
// preserve the default workspace ID?") without depending on a literal
// embedded in another test file.
const (
	DefaultWorkspaceID = "ws_test"
	DefaultAgentID     = "agent_test"
	DefaultCrewID      = "crew_test"
	DefaultTier        = "AGENT"
	DefaultKey         = ""
	DefaultMode        = "replace"
	DefaultQuery       = "test query"
	DefaultLimit       = 10
)

// RetainOpt mutates a RetainRequest under construction. Function-option
// pattern is preferred over a giant struct argument because it lets a
// test only restate the fields that matter ("the default request, but
// with Mode=append") instead of carrying every default forward.
type RetainOpt func(*memory.RetainRequest)

// BuildRetainRequest returns a [memory.RetainRequest] with sensible
// defaults. Each opt mutates the request in order; later opts win on
// conflict. The defaults are chosen so the result is valid against
// [memory.LocalDispatcher] — a test that just needs "some Retain
// request" can call this with no args.
func BuildRetainRequest(opts ...RetainOpt) memory.RetainRequest {
	req := memory.RetainRequest{
		WorkspaceID: DefaultWorkspaceID,
		AgentID:     DefaultAgentID,
		CrewID:      DefaultCrewID,
		Tier:        DefaultTier,
		Key:         DefaultKey,
		Content:     "test content",
		Mode:        DefaultMode,
	}
	for _, opt := range opts {
		opt(&req)
	}
	return req
}

// WithRetainWorkspace overrides the workspace id on a Retain fixture.
func WithRetainWorkspace(id string) RetainOpt { return func(r *memory.RetainRequest) { r.WorkspaceID = id } }

// WithRetainAgent overrides the agent id on a Retain fixture.
func WithRetainAgent(id string) RetainOpt { return func(r *memory.RetainRequest) { r.AgentID = id } }

// WithRetainTier overrides the tier on a Retain fixture.
func WithRetainTier(tier string) RetainOpt { return func(r *memory.RetainRequest) { r.Tier = tier } }

// WithRetainContent overrides the content body on a Retain fixture.
func WithRetainContent(c string) RetainOpt { return func(r *memory.RetainRequest) { r.Content = c } }

// WithRetainMode switches between replace and append.
func WithRetainMode(mode string) RetainOpt { return func(r *memory.RetainRequest) { r.Mode = mode } }

// WithRetainKey sets the tier-specific key (date for daily, slug for peers).
func WithRetainKey(key string) RetainOpt { return func(r *memory.RetainRequest) { r.Key = key } }

// RecallOpt mutates a RecallRequest under construction.
type RecallOpt func(*memory.RecallRequest)

// BuildRecallRequest returns a [memory.RecallRequest] with sensible
// defaults — single workspace + agent, no tier scope, limit 10.
func BuildRecallRequest(opts ...RecallOpt) memory.RecallRequest {
	req := memory.RecallRequest{
		WorkspaceID: DefaultWorkspaceID,
		AgentID:     DefaultAgentID,
		CrewID:      DefaultCrewID,
		Tier:        "",
		Query:       DefaultQuery,
		Limit:       DefaultLimit,
	}
	for _, opt := range opts {
		opt(&req)
	}
	return req
}

// WithRecallQuery overrides the query string.
func WithRecallQuery(q string) RecallOpt { return func(r *memory.RecallRequest) { r.Query = q } }

// WithRecallTier limits the recall to a specific tier.
func WithRecallTier(tier string) RecallOpt { return func(r *memory.RecallRequest) { r.Tier = tier } }

// WithRecallLimit overrides the snippet count cap.
func WithRecallLimit(n int) RecallOpt { return func(r *memory.RecallRequest) { r.Limit = n } }

// BuildSnippet returns a deterministic [memory.RecallSnippet]. Source
// uses the same shape the local dispatcher emits (tier-relative label,
// no absolute path leakage). Score defaults to 1.0 to match the local
// dispatcher's no-rank convention; remote-provider tests should pass
// an explicit score.
func BuildSnippet(source, snippet string) memory.RecallSnippet {
	return memory.RecallSnippet{
		Source:  source,
		Snippet: snippet,
		Score:   1.0,
	}
}

// BuildSnippets is a tiny convenience for building N identical-shape
// snippets with predictable source labels — useful for asserting "the
// caller capped at Limit" without caring about content variation.
func BuildSnippets(n int) []memory.RecallSnippet {
	out := make([]memory.RecallSnippet, n)
	for i := 0; i < n; i++ {
		out[i] = BuildSnippet(
			fmt.Sprintf("daily/2026-05-%02d.md", i+1),
			fmt.Sprintf("snippet %d body", i+1),
		)
	}
	return out
}

// ─── MockProvider ────────────────────────────────────────────────────────

// MockProvider is a thread-safe [memory.Provider] implementation
// driven by knobs the test sets up front. Use it when the code under
// test calls into the Provider interface and you need deterministic
// responses (success / failure / slow / empty / oversize).
//
// Zero-value MockProvider returns empty successful results from every
// method. Configure failure modes via the Set* methods before exercising
// the code under test.
type MockProvider struct {
	mu sync.Mutex

	// Configured behaviour — nil means "use default success path".
	retainErr  error
	recallErr  error
	forgetErr  error
	healthErr  error
	retainResp *memory.RetainResult
	recallResp *memory.RecallResult
	forgetResp *memory.ForgetResult
	healthResp *memory.HealthStatus

	// Optional delays so timeout-sensitive code paths can be exercised.
	retainDelay time.Duration
	recallDelay time.Duration
	forgetDelay time.Duration
	healthDelay time.Duration

	// Call counters — exposed via the *Calls() accessors so tests can
	// assert "Recall was called exactly twice" without reaching into
	// the mock's private state.
	retainCalls []memory.RetainRequest
	recallCalls []memory.RecallRequest
	forgetCalls []memory.ForgetRequest
	healthCalls int
}

// NewMockProvider returns a ready-to-use mock with the default-success
// behaviour for every method. Configure overrides via SetRetain*, etc.
func NewMockProvider() *MockProvider { return &MockProvider{} }

// SetRetainError makes the next Retain call return err. Pass nil to
// clear the override and restore the default success behaviour.
func (m *MockProvider) SetRetainError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retainErr = err
}

// SetRetainResponse makes the next Retain call return r and nil error.
// Overrides SetRetainError if both are set; error wins on the wire if
// the test wants "returned a result but the wrapper translated to err".
func (m *MockProvider) SetRetainResponse(r memory.RetainResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retainResp = &r
}

// SetRetainDelay introduces a per-call sleep so timeout-bounded code
// paths can be exercised against a known clock budget. Zero clears.
func (m *MockProvider) SetRetainDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retainDelay = d
}

// SetRecallError mirrors [SetRetainError] for the Recall path.
func (m *MockProvider) SetRecallError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallErr = err
}

// SetRecallResponse mirrors [SetRetainResponse] for the Recall path.
func (m *MockProvider) SetRecallResponse(r memory.RecallResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallResp = &r
}

// SetRecallDelay mirrors [SetRetainDelay] for the Recall path.
func (m *MockProvider) SetRecallDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recallDelay = d
}

// SetForgetError mirrors [SetRetainError] for the Forget path.
func (m *MockProvider) SetForgetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetErr = err
}

// SetForgetResponse mirrors [SetRetainResponse] for the Forget path.
func (m *MockProvider) SetForgetResponse(r memory.ForgetResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetResp = &r
}

// SetForgetDelay mirrors [SetRetainDelay] for the Forget path.
func (m *MockProvider) SetForgetDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetDelay = d
}

// SetHealthStatus pins the response from the next Health call. Use
// this with OK=false + Message to exercise the aux-status panel's
// degraded-backend path.
func (m *MockProvider) SetHealthStatus(s memory.HealthStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthResp = &s
}

// Retain implements [memory.Provider]. Honours the configured delay
// (including context cancellation during the sleep) and error, records
// the request for later assertions.
func (m *MockProvider) Retain(ctx context.Context, req memory.RetainRequest) (memory.RetainResult, error) {
	m.mu.Lock()
	delay := m.retainDelay
	err := m.retainErr
	resp := m.retainResp
	m.retainCalls = append(m.retainCalls, req)
	m.mu.Unlock()

	if err := sleepCtx(ctx, delay); err != nil {
		return memory.RetainResult{}, err
	}
	if err != nil {
		return memory.RetainResult{}, err
	}
	if resp != nil {
		return *resp, nil
	}
	// Default-success path: produce a stable canonical id from tier+key
	// so a test asserting "the caller fed result.ID back to Forget"
	// can match without configuring an explicit SetRetainResponse.
	id := req.Tier
	if req.Key != "" {
		id = req.Tier + "/" + req.Key
	}
	return memory.RetainResult{ID: id, Bytes: len(req.Content)}, nil
}

// Recall implements [memory.Provider]. Same delay+error pattern as Retain.
func (m *MockProvider) Recall(ctx context.Context, req memory.RecallRequest) (memory.RecallResult, error) {
	m.mu.Lock()
	delay := m.recallDelay
	err := m.recallErr
	resp := m.recallResp
	m.recallCalls = append(m.recallCalls, req)
	m.mu.Unlock()

	if err := sleepCtx(ctx, delay); err != nil {
		return memory.RecallResult{}, err
	}
	if err != nil {
		return memory.RecallResult{}, err
	}
	if resp != nil {
		return *resp, nil
	}
	return memory.RecallResult{Hits: []memory.RecallSnippet{}}, nil
}

// Forget implements [memory.Provider]. Same delay+error pattern as Retain.
func (m *MockProvider) Forget(ctx context.Context, req memory.ForgetRequest) (memory.ForgetResult, error) {
	m.mu.Lock()
	delay := m.forgetDelay
	err := m.forgetErr
	resp := m.forgetResp
	m.forgetCalls = append(m.forgetCalls, req)
	m.mu.Unlock()

	if err := sleepCtx(ctx, delay); err != nil {
		return memory.ForgetResult{}, err
	}
	if err != nil {
		return memory.ForgetResult{}, err
	}
	if resp != nil {
		return *resp, nil
	}
	return memory.ForgetResult{Removed: 0}, nil
}

// Health implements [memory.Provider]. Health is non-mutating so it
// records call count rather than the full request payload.
func (m *MockProvider) Health(ctx context.Context) memory.HealthStatus {
	m.mu.Lock()
	delay := m.healthDelay
	resp := m.healthResp
	m.healthCalls++
	m.mu.Unlock()

	_ = sleepCtx(ctx, delay)
	if resp != nil {
		return *resp
	}
	return memory.HealthStatus{OK: true, CheckedAt: time.Now()}
}

// RetainCalls returns a defensive copy of every Retain request the
// mock observed, in invocation order. Defensive-copy so a test that
// inspects results then mutates them can't corrupt the mock's
// internal state.
func (m *MockProvider) RetainCalls() []memory.RetainRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memory.RetainRequest, len(m.retainCalls))
	copy(out, m.retainCalls)
	return out
}

// RecallCalls returns a defensive copy of every Recall request.
func (m *MockProvider) RecallCalls() []memory.RecallRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memory.RecallRequest, len(m.recallCalls))
	copy(out, m.recallCalls)
	return out
}

// ForgetCalls returns a defensive copy of every Forget request.
func (m *MockProvider) ForgetCalls() []memory.ForgetRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memory.ForgetRequest, len(m.forgetCalls))
	copy(out, m.forgetCalls)
	return out
}

// HealthCalls returns the number of Health invocations observed.
func (m *MockProvider) HealthCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthCalls
}

// Reset clears all configured behaviours AND every recorded call.
// Use between subtests when the same mock should look fresh for each.
func (m *MockProvider) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retainErr, m.recallErr, m.forgetErr, m.healthErr = nil, nil, nil, nil
	m.retainResp, m.recallResp, m.forgetResp, m.healthResp = nil, nil, nil, nil
	m.retainDelay, m.recallDelay, m.forgetDelay, m.healthDelay = 0, 0, 0, 0
	m.retainCalls = nil
	m.recallCalls = nil
	m.forgetCalls = nil
	m.healthCalls = 0
}

// sleepCtx sleeps for d, returning early with ctx.Err() if the
// context is cancelled first. Zero d is a no-op. The intent is that a
// test setting SetRetainDelay(5*time.Second) and a code-under-test
// passing a 1-second-deadline context sees context.DeadlineExceeded
// rather than the full sleep.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// ─── Edge case catalog ──────────────────────────────────────────────────

// EdgeCase documents a recurring memory failure mode along with a
// short recipe a future test can copy-paste. The Slug is a stable
// identifier (kebab-case) used for searchability — grep the codebase
// for "edge_case_oversize_retain" to find every test that explicitly
// covers that case.
type EdgeCase struct {
	Slug        string
	Description string
	Recipe      string // pseudocode showing how to set up MockProvider
}

// EdgeCases enumerates the canonical memory failure modes worth a
// regression test. Add to this list when a new class of bug surfaces
// — the entry becomes both documentation and a coverage tracker.
var EdgeCases = []EdgeCase{
	{
		Slug:        "edge_case_empty_recall",
		Description: "Recall returns zero hits — caller must not crash on nil iteration",
		Recipe:      "mock.SetRecallResponse(memory.RecallResult{Hits: []memory.RecallSnippet{}})",
	},
	{
		Slug:        "edge_case_oversize_retain",
		Description: "Retain content exceeds the per-tier cap — caller must surface cap rejection",
		Recipe:      "mock.SetRetainError(memory.ErrCapExceeded)",
	},
	{
		Slug:        "edge_case_provider_timeout",
		Description: "Provider call exceeds the caller's context deadline — caller must propagate ctx.Err()",
		Recipe:      "mock.SetRecallDelay(2 * time.Second); ctx, _ := context.WithTimeout(parent, 100*time.Millisecond)",
	},
	{
		Slug:        "edge_case_health_degraded",
		Description: "Health reports OK=false — aux-status panel must surface the message verbatim",
		Recipe:      "mock.SetHealthStatus(memory.HealthStatus{OK: false, Message: \"index rebuild in progress\"})",
	},
	{
		Slug:        "edge_case_forget_no_match",
		Description: "Forget selector matches nothing — caller must treat Removed=0 as success not failure",
		Recipe:      "// default mock returns ForgetResult{Removed: 0}, nil — exercise the caller's success branch",
	},
	{
		Slug:        "edge_case_concurrent_retain",
		Description: "Two workers Retain to the same key — provider must serialise without corruption",
		Recipe:      "// spawn N goroutines calling mock.Retain in parallel; assert RetainCalls() length == N",
	},
	{
		Slug:        "edge_case_quarantine_in_recall",
		Description: "Recall returns Quarantined entries — caller must surface but NEVER feed to model",
		Recipe:      "mock.SetRecallResponse(memory.RecallResult{Hits: ..., Quarantined: []string{\"persona/poisoned.md\"}})",
	},
	{
		Slug:        "edge_case_unicode_content",
		Description: "Content contains multi-byte UTF-8 (emoji, RTL, zero-width) — round-trip must preserve bytes",
		Recipe:      "req := memtest.BuildRetainRequest(memtest.WithRetainContent(\"héllo 🦀 שלום\\u200B\"))",
	},
	{
		Slug:        "edge_case_empty_tier",
		Description: "Recall request with Tier=\"\" must return hits across all accessible tiers",
		Recipe:      "req := memtest.BuildRecallRequest() // default Tier is empty",
	},
	{
		Slug:        "edge_case_workspace_scope_violation",
		Description: "Caller passes a workspace id different from the dispatcher's binding — must fail loud, not silently route",
		Recipe:      "req := memtest.BuildRetainRequest(memtest.WithRetainWorkspace(\"other_workspace\"))",
	},
}

// EdgeCaseBySlug returns the catalog entry for slug or nil. Use in
// table-driven tests so an unknown slug fails the test rather than
// silently being a no-op.
func EdgeCaseBySlug(slug string) *EdgeCase {
	for i := range EdgeCases {
		if EdgeCases[i].Slug == slug {
			return &EdgeCases[i]
		}
	}
	return nil
}

// EdgeCaseSlugs returns every slug in the catalog, sorted in the
// catalog's declaration order. Useful for tests that want to iterate
// every known edge case and assert the system handles each.
func EdgeCaseSlugs() []string {
	out := make([]string, len(EdgeCases))
	for i, ec := range EdgeCases {
		out[i] = ec.Slug
	}
	return out
}

// ─── Workspace temp-dir helper ──────────────────────────────────────────

// WorkspaceLayout describes the on-disk layout a memory test typically
// needs: a workspace root with agent and crew subdirectories already
// created at the standard 0700 permissions.
type WorkspaceLayout struct {
	Root      string
	AgentDir  string // <root>/agents/<agent>
	CrewDir   string // <root>/crew
	PersonaMD string // <agent_dir>/PERSONA.md (created empty)
	AgentMD   string // <agent_dir>/AGENT.md (created empty)
}

// String returns the root path for logging.
func (w WorkspaceLayout) String() string {
	return fmt.Sprintf("WorkspaceLayout{Root=%s}", w.Root)
}

// DescribeEdgeCases returns a markdown bullet list of the catalog,
// useful for embedding into a test failure message that lists the
// known scenarios next to the one that failed.
func DescribeEdgeCases() string {
	var b strings.Builder
	for _, ec := range EdgeCases {
		fmt.Fprintf(&b, "- %s: %s\n", ec.Slug, ec.Description)
	}
	return b.String()
}
