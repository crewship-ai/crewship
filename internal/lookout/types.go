// Package lookout provides defensive guardrails for agent inputs and outputs.
//
// It is composed of four layers, each independently usable:
//
//  1. injection — heuristic prompt-injection detector (role-override,
//     system-prompt-leak, jailbreak tropes, confusable unicode).
//  2. args      — JSON Schema validation of tool-call arguments before they
//     reach the tool implementation.
//  3. output    — structured-output parser that strips markdown fences,
//     validates against schema, and produces a corrective re-prompt.
//  4. secrets   — regex-based secrets redactor for outbound text.
//
// All layers are purely in-process: no network calls in the default build.
// An optional Lakera bridge is offered for the injection layer but is
// disabled unless WithLakeraAPIKey is wired in by the caller.
//
// When a guard blocks something it emits a journal entry of type
// EntryGuardrailInput or EntryGuardrailOutput so the action is auditable.
// The matched secret value itself is NEVER included in the entry payload —
// only the kind of finding and a stable redacted detail string.
package lookout

// Verdict expresses the final decision a guard made about a payload.
//
// Allow    — nothing matched; pass the payload through unchanged.
// Block    — at least one finding has severity high enough that the caller
//
//	should refuse to process the payload.
//
// Sanitize — findings exist but the guard produced a cleaned version
//
//	(currently only the secrets layer returns this).
type Verdict string

const (
	VerdictAllow    Verdict = "allow"
	VerdictBlock    Verdict = "block"
	VerdictSanitize Verdict = "sanitize"
)

// Severity classifies how seriously a finding should be treated. The
// middleware maps these to journal severities (low->info, medium->notice,
// high->warn, critical->error).
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Kind identifies the category of finding. Strings are stable: downstream
// dashboards group by kind.
type Kind string

const (
	KindRoleOverride      Kind = "role_override"
	KindSystemPromptLeak  Kind = "system_prompt_leak"
	KindJailbreak         Kind = "jailbreak"
	KindZeroWidth         Kind = "zero_width_unicode"
	KindRTLOverride       Kind = "rtl_override_unicode"
	KindSecretAPIKey      Kind = "secret_api_key"
	KindSecretOpenAI      Kind = "secret_openai"
	KindSecretAnthropic   Kind = "secret_anthropic"
	KindSecretAWS         Kind = "secret_aws"
	KindSecretGitHubPAT   Kind = "secret_github_pat"
	KindSecretGitHubOAuth Kind = "secret_github_oauth"
	KindSecretGitHubApp   Kind = "secret_github_app"
	KindSecretBearer      Kind = "secret_bearer_token"
	KindSecretPassword    Kind = "secret_password"
	KindLakeraDetected    Kind = "lakera_detected"
)

// Finding is a single hit produced by a scanner. Detail is a short
// human-readable description; Matched is the substring that triggered the
// rule (NEVER set this to a raw secret — secrets layer fills it with the
// kind only). Position is the byte offset into the scanned text or -1 if
// not applicable.
//
// MatchEnd is the byte offset one past the last byte of the actual
// match in the source text. It is the AUTHORITATIVE replacement span
// for sanitization — Matched is for human display (and gets truncated
// for long hits, plus carries synthetic strings like "U+202E" for
// unicode findings) so a sanitize-mode redaction cannot rely on it.
// Position == MatchEnd means "no byte range" (synthetic finding from
// the secrets scanner) and sanitize must fall back to leaving the
// text alone for that finding.
type Finding struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail"`
	Matched  string   `json:"matched,omitempty"`
	Position int      `json:"position"`
	MatchEnd int      `json:"match_end,omitempty"`
}

// ScanResult bundles the findings and the resulting verdict for a single
// scan invocation.
type ScanResult struct {
	Findings []Finding `json:"findings"`
	Verdict  Verdict   `json:"verdict"`
}

// HighestSeverity returns the most severe finding's severity. Returns
// SeverityLow when there are no findings — callers should usually check
// len(Findings) first.
func (r ScanResult) HighestSeverity() Severity {
	rank := map[Severity]int{
		SeverityLow:      1,
		SeverityMedium:   2,
		SeverityHigh:     3,
		SeverityCritical: 4,
	}
	best := SeverityLow
	for _, f := range r.Findings {
		if rank[f.Severity] > rank[best] {
			best = f.Severity
		}
	}
	return best
}
