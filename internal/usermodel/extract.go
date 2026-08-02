package usermodel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/llm"
	"github.com/crewship-ai/crewship/internal/memory"
)

// Resolver hands back the provider and model for the NEXT extraction.
//
// Call sites hold one of these rather than a resolved pair, for the same
// reason internal/runverdict does (#1556): the aux slot behind this is
// runtime-settable, and a pair captured at construction is a pair no
// override can reach without a server restart.
//
// A nil provider means "extraction is off" — an unconfigured or
// unbuildable slot, e.g. no ANTHROPIC_API_KEY — not an error.
type Resolver func() (llm.Provider, string)

// ProfileReader resolves the configured extraction profile. Read per
// sweep so `crewship instance settings set memory.user_model_profile`
// takes effect on the next sweep rather than on the next restart.
type ProfileReader func(ctx context.Context) Profile

// Extractor implements consolidate.UserModelExtractor.
//
// One sweep candidate in, a merged-ready set of bullets out. Everything
// it returns has been through Verify; everything it refused is counted
// on the Extractor and logged, because a refusal rate is the only thing
// that distinguishes a working stated-only extractor from a broken one.
type Extractor struct {
	db       *sql.DB
	resolve  Resolver
	profile  ProfileReader
	logger   *slog.Logger
	lookback time.Duration
	maxTurns int
	now      func() time.Time
}

// Option configures an Extractor.
type Option func(*Extractor)

// WithLookback bounds how far back a transcript is read. Defaults to the
// sweep's own 14-day window.
func WithLookback(d time.Duration) Option {
	return func(e *Extractor) {
		if d > 0 {
			e.lookback = d
		}
	}
}

// WithMaxTurns bounds one extraction's transcript length.
func WithMaxTurns(n int) Option {
	return func(e *Extractor) {
		if n > 0 {
			e.maxTurns = n
		}
	}
}

// WithClock injects a clock for tests.
func WithClock(now func() time.Time) Option {
	return func(e *Extractor) {
		if now != nil {
			e.now = now
		}
	}
}

// New builds the production extractor. resolve and profile may not be
// nil; db may not be nil (the transcript is the evidence, and without it
// nothing is admissible).
func New(db *sql.DB, resolve Resolver, profile ProfileReader, logger *slog.Logger, opts ...Option) *Extractor {
	e := &Extractor{
		db:       db,
		resolve:  resolve,
		profile:  profile,
		logger:   logger,
		lookback: 14 * 24 * time.Hour,
		maxTurns: DefaultMaxTurns,
		now:      time.Now,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// maxCompletionTokens bounds the model's reply. Nine fields, each a short
// value plus its evidence span, is comfortably under this; the bound is
// here so a model that decides to narrate cannot bill for it.
const maxCompletionTokens = 1200

// Extract satisfies consolidate.UserModelExtractor.
//
// Returns ("", nil) — which the sweep records as skip_empty_content —
// whenever there is nothing to write. That is the COMMON case under a
// stated-only policy and is not a failure: most days, most people state
// no new durable fact about themselves. An error is returned only when
// something that should have worked did not.
func (e *Extractor) Extract(ctx context.Context, cand consolidate.UserModelCandidate, prior string) (string, error) {
	if e == nil || e.db == nil || e.resolve == nil || e.profile == nil {
		return "", nil
	}
	p := e.profile(ctx)
	if !p.Writes() {
		return "", nil
	}

	turns, err := LoadTranscript(ctx, e.db, cand.WorkspaceID, cand.UserID,
		e.now().Add(-e.lookback), e.maxTurns)
	if err != nil {
		return "", err
	}
	if !HasSubjectTurns(turns) {
		// Nothing the person said, so nothing is admissible. Skipping the
		// model call here is not an optimisation for its own sake: it is
		// the only answer a stated-only policy can give, and paying a
		// model to produce it daily for every quiet operator is waste.
		return "", nil
	}

	provider, model := e.resolve()
	if provider == nil {
		// The slot is unconfigured or unbuildable. "Feature off", not an
		// error — a later fix to the wiring is picked up on the next sweep.
		return "", nil
	}

	resp, err := provider.Complete(ctx, llm.Request{
		Model:     model,
		System:    BuildSystemPrompt(p),
		MaxTokens: maxCompletionTokens,
		Messages:  []llm.Message{{Role: llm.RoleUser, Content: BuildUserMessage(prior, turns)}},
	})
	if err != nil {
		return "", fmt.Errorf("usermodel: complete: %w", err)
	}

	cands, err := ParseCandidates(resp.Content)
	if err != nil {
		return "", err
	}
	facts, refused := Verify(p, turns, cands)

	if e.logger != nil && (len(facts) > 0 || len(refused) > 0) {
		// user_slug, never user_id: the whole storage layer is
		// deliberately PII-free and a log line is a directory listing
		// with extra steps.
		e.logger.Info("user model extraction",
			"workspace_id", cand.WorkspaceID,
			"user_slug", memory.UserSlug(cand.UserID, cand.WorkspaceID),
			"profile", p.Name,
			"written", len(facts),
			"refused", len(refused),
			"reasons", refusalReasons(refused))
	}
	return Render(p, facts), nil
}

// refusalReasons collapses refusals to a reason→count map for one log
// line. Values are deliberately absent: a refused value is frequently
// the most sensitive thing the extraction produced (it is, after all,
// the thing judged unfit to store) and it must not survive in a log.
func refusalReasons(rs []Refusal) map[string]int {
	if len(rs) == 0 {
		return nil
	}
	out := make(map[string]int, len(rs))
	for _, r := range rs {
		out[r.Reason]++
	}
	return out
}

// Render turns verified facts into the "- key: value" bullet shape the
// on-disk model uses and consolidate.MergeUserModel merges on, ordered by
// the profile's key priority so the file is stable across sweeps.
//
// The evidence quote is NOT rendered. It is verification input, not
// content: the file has a 1.5 KB cap read into every prompt, and doubling
// each fact to carry its own provenance would halve how much a person can
// be known by. That provenance is worth surfacing to the PERSON, which is
// a store this file cannot be — see the user-model read/correct surface.
func Render(p Profile, facts []Fact) string {
	if len(facts) == 0 {
		return ""
	}
	byKey := make(map[string]string, len(facts))
	for _, f := range facts {
		byKey[f.Key] = f.Value
	}
	var b strings.Builder
	for _, k := range p.KeyOrder() {
		if v, ok := byKey[k]; ok {
			b.WriteString("- " + k + ": " + v + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ParseCandidates reads the model's reply.
//
// Tolerant of the wrappers models add — a markdown fence, a leading
// "Here's what I found:" — by scanning for the outermost JSON object,
// the same shape internal/runverdict uses. Intolerant of anything else:
// a reply that carries no object at all is an error, because silently
// treating a malformed reply as "no facts today" would make a broken
// extractor indistinguishable from a working one with nothing to say.
func ParseCandidates(raw string) ([]Candidate, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("usermodel: empty reply from the model")
	}
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("usermodel: no JSON object in reply (%d bytes)", len(s))
	}
	var payload struct {
		Facts []Candidate `json:"facts"`
	}
	if err := json.Unmarshal([]byte(s[start:end+1]), &payload); err != nil {
		return nil, fmt.Errorf("usermodel: parse reply: %w", err)
	}
	return payload.Facts, nil
}
