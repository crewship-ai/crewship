package hooks

import (
	"regexp"
	"sync"
)

// EventContext carries the runtime fields a Matcher evaluates against. Not
// every event populates every field — PreAgentStart has no ToolName,
// PostLLMCall has no GuardrailSeverity — so Matcher.Matches treats unset
// fields as "match anything".
type EventContext struct {
	// Event that triggered dispatch. Informational; callers already
	// filtered by event when they called ListByEvent, but handlers use
	// this for logging and webhook bodies.
	Event Event

	// Scope. AgentID/CrewID are used both by Matcher.Matches and by
	// handler env vars / HTTP bodies.
	WorkspaceID string
	CrewID      string
	AgentID     string
	MissionID   string

	// Tool-call events only.
	ToolName string

	// LLM-call events only.
	LLMProvider string
	LLMModel    string
	CostUSD     float64

	// Guardrail / severity-bearing events (budget, guardrail).
	Severity string

	// Payload is the event-specific structured data passed to handlers.
	// It rides through to shell env (CREWSHIP_PAYLOAD) and HTTP bodies
	// verbatim; the matcher does not inspect it.
	Payload map[string]any
}

// Matches evaluates m against ctx. Every populated slice in the matcher
// must have at least one element that matches the corresponding field;
// empty slices are "don't care". The zero-value Matcher matches everything.
func Matches(m Matcher, ctx EventContext) bool {
	if len(m.Tools) > 0 {
		if ctx.ToolName == "" || !anyRegex(m.Tools, ctx.ToolName) {
			return false
		}
	}
	if len(m.AgentIDs) > 0 {
		if !contains(m.AgentIDs, ctx.AgentID) {
			return false
		}
	}
	if len(m.CrewIDs) > 0 {
		if !contains(m.CrewIDs, ctx.CrewID) {
			return false
		}
	}
	if len(m.Severities) > 0 {
		if !contains(m.Severities, ctx.Severity) {
			return false
		}
	}
	// m.When is reserved for future CEL/expr evaluation; ignore today.
	return true
}

// regexCache stores compiled patterns so hot-path matching doesn't
// re-compile every call. The set of distinct patterns is bounded by
// hook count, which is tiny relative to event volume, so an unbounded
// sync.Map is fine here.
var regexCache sync.Map // map[string]*regexp.Regexp

func compileRegex(pat string) *regexp.Regexp {
	if v, ok := regexCache.Load(pat); ok {
		return v.(*regexp.Regexp)
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		// Cache a nil sentinel so subsequent calls skip re-parsing the
		// same bad pattern. Matchers treat a nil regex as "no match".
		regexCache.Store(pat, (*regexp.Regexp)(nil))
		return nil
	}
	regexCache.Store(pat, re)
	return re
}

func anyRegex(patterns []string, s string) bool {
	for _, p := range patterns {
		re := compileRegex(p)
		if re == nil {
			continue
		}
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
