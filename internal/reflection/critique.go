package reflection

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LLMClient is the minimal surface this package needs from any LLM
// provider. Keeping the interface here (rather than importing
// internal/llm) means the package can be exercised in tests with a
// trivial stub and has no provider-specific dependencies.
//
// The Call contract:
//   - systemPrompt is a persona's role framing (see personas.go).
//   - userPrompt wraps Subject + Context.
//   - The implementation is expected to return the raw response text,
//     which should (but is not guaranteed to) be JSON matching the
//     schema described in personaPrompts.
//   - Errors MUST propagate. The critique layer does not wrap network
//     or upstream errors into a "safe" fallback critique because doing
//     so would hide outage signals that the caller needs to act on.
type LLMClient interface {
	Call(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// Critiquer is the production boundary the orchestrator calls. Tests
// substitute a stub that returns canned critiques without touching an
// LLM. Exposing an interface instead of a struct lets the orchestrator
// stay the same whether critiques are produced by an LLM, a rules
// engine, or a pre-recorded fixture.
type Critiquer interface {
	Critique(ctx context.Context, persona Persona, subject string, critCtx string) (Critique, error)
}

// LLMCritiquer is the production Critiquer. It wraps an LLMClient and
// knows how to (a) build a persona-framed prompt, (b) call the client,
// and (c) parse the response into a Critique. Parse failures are
// tolerated: the raw response lands in Critique.RawText and severity is
// pinned to low so the synthesizer downstream is not blown off course by
// a mangled response.
type LLMCritiquer struct {
	Client LLMClient
}

// NewLLMCritiquer returns a Critiquer wired to the given client. The
// client must be non-nil; a nil client is treated as a programming error
// because every call site has a client (even if it's a stub).
func NewLLMCritiquer(client LLMClient) *LLMCritiquer {
	return &LLMCritiquer{Client: client}
}

// Critique asks the LLM to review the subject from the persona's point of
// view and parses the response. The returned Critique always has Persona
// set even on parse failure, so downstream code doesn't need to worry
// about a zero-value persona.
func (c *LLMCritiquer) Critique(ctx context.Context, persona Persona, subject string, critCtx string) (Critique, error) {
	if c == nil || c.Client == nil {
		return Critique{}, fmt.Errorf("reflection: LLMCritiquer has no client")
	}
	systemPrompt := SystemPromptFor(persona)
	userPrompt := buildUserPrompt(subject, critCtx)

	raw, err := c.Client.Call(ctx, systemPrompt, userPrompt)
	if err != nil {
		return Critique{Persona: persona}, fmt.Errorf("reflection: llm call for %s: %w", persona, err)
	}

	return parseCritique(persona, raw), nil
}

// buildUserPrompt glues Subject and Context together in a consistent
// layout. Both fields are marked with delimiters so a persona prompt
// can't be tricked (for instance, by a subject that embeds "context:"
// headers) into conflating the two.
func buildUserPrompt(subject, critCtx string) string {
	var b strings.Builder
	b.WriteString("===SUBJECT===\n")
	b.WriteString(subject)
	if !strings.HasSuffix(subject, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("===END SUBJECT===\n\n")
	if critCtx != "" {
		b.WriteString("===CONTEXT===\n")
		b.WriteString(critCtx)
		if !strings.HasSuffix(critCtx, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("===END CONTEXT===\n")
	}
	return b.String()
}

// parseCritique decodes the JSON envelope we instructed the LLM to emit
// (see personas.jsonContract). We try a few permissive strategies:
//
//  1. Direct json.Unmarshal of the trimmed response.
//  2. If that fails, try to extract the first {...} block and re-parse
//     it. LLMs occasionally wrap JSON in a markdown fence despite being
//     told not to.
//
// If both fail, return a low-severity critique carrying the raw text so
// the synthesizer still sees something and the operator can inspect the
// failure mode.
func parseCritique(persona Persona, raw string) Critique {
	trimmed := strings.TrimSpace(raw)
	var decoded struct {
		Severity    string   `json:"severity"`
		Issues      []string `json:"issues"`
		Suggestions []string `json:"suggestions"`
	}

	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		if block, ok := extractJSONBlock(trimmed); ok {
			if err := json.Unmarshal([]byte(block), &decoded); err != nil {
				return Critique{
					Persona:  persona,
					Severity: CritiqueSeverityLow,
					RawText:  raw,
				}
			}
		} else {
			return Critique{
				Persona:  persona,
				Severity: CritiqueSeverityLow,
				RawText:  raw,
			}
		}
	}

	return Critique{
		Persona:     persona,
		Severity:    normalizeSeverity(decoded.Severity),
		Issues:      decoded.Issues,
		Suggestions: decoded.Suggestions,
		RawText:     raw,
	}
}

// extractJSONBlock finds the first balanced {...} block in the text. It
// is deliberately simple-minded — no escaped-brace awareness — because
// the prompts restrict JSON to a flat object where escaped braces inside
// strings are rare. A sophisticated parser here would mostly hide
// problems we'd rather see.
func extractJSONBlock(s string) (string, bool) {
	start := strings.Index(s, "{")
	if start < 0 {
		return "", false
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// normalizeSeverity coerces the LLM's severity string into our typed
// enum. Unknown values default to low — the same bias as parse-failure
// critiques, so one persona can't unilaterally escalate the review by
// inventing a severity label.
func normalizeSeverity(s string) CritiqueSeverity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return CritiqueSeverityCritical
	case "high":
		return CritiqueSeverityHigh
	case "medium", "med":
		return CritiqueSeverityMedium
	case "low", "":
		return CritiqueSeverityLow
	default:
		return CritiqueSeverityLow
	}
}
