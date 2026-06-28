package inbox

import (
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/lookout"
)

// This file owns the presentation hygiene for inbox rows: turning the
// engine-internal strings that source handlers hand us (a gatekeeper's
// decision Reason, a raw approval prompt, an agent's escalation context)
// into something safe and readable for a human in the /inbox UI.
//
// Three concerns, three helpers:
//   - CleanTitle   — collapse a multi-line markdown body into one tidy line
//   - SanitizeReason — strip leaked plumbing errors, flag infra outages
//   - RedactSecrets — mask credential material so it never lands in body_md
//
// All three are pure string functions so they're trivially unit-tested
// and reusable across the pipeline / api / server packages that emit rows.

// internalErrorMarkers are substrings that betray a Keeper/LLM plumbing
// failure rather than a real, actionable finding. When a curator's
// "reason" contains one of these, the advisory exists only because the
// model couldn't run — it's an infrastructure hiccup, not something the
// operator decided to surface. We use this to (a) replace the raw text
// with a friendly line and (b) let pure-advisory call sites suppress the
// inbox row entirely instead of spamming one per crew per sweep.
//
// Matched case-insensitively. Kept DELIBERATELY NARROW — strong infra
// signatures only ("the curator/LLM couldn't run"). Loose single words
// like "unconfigured" / "deny by default" / "workspace_id required" were
// removed: a real memory-health finding ("references an unconfigured
// deployment target") must not be matched as an outage and silently
// dropped. Every marker here is a phrase a genuine finding won't contain.
var internalErrorMarkers = []string{
	"llm unavailable",
	"llm not configured",
	"llm error",
	"llm returned",
	"curator unavailable",
	"unparseable response",
}

// infraFriendly is the user-facing replacement for a leaked plumbing
// error. Says what happened (review couldn't run), reassures (no action
// lost), and sets the expectation (manual review / next cycle).
const infraFriendly = "Automatic review couldn't run — the curator service is temporarily unavailable. Falling back to manual review; no action is lost."

// SanitizeReason converts a gatekeeper/curator decision Reason into a
// body safe to show a human. It returns the cleaned text plus an `infra`
// flag: true when the reason is an infrastructure outage (LLM down /
// unconfigured / unparseable) rather than a real finding.
//
// When infra: callers that are pure advisories ("this crew's memory looks
// unhealthy") should SKIP creating an inbox row and just log — the row
// carries no operator-actionable signal. Callers that represent a real
// pending item regardless of the model (a skill that still needs review)
// keep the row but show the friendly text and stash the raw reason in
// payload for operators / logs.
//
// When not infra the reason is a genuine finding: returned as-is but with
// secrets redacted as defense-in-depth (a finding might quote a value).
func SanitizeReason(reason string) (friendly string, infra bool) {
	trimmed := strings.TrimSpace(reason)
	low := strings.ToLower(trimmed)
	for _, m := range internalErrorMarkers {
		if strings.Contains(low, m) {
			return infraFriendly, true
		}
	}
	return RedactSecrets(trimmed), false
}

// connURIRe matches credential-bearing connection strings like
// redis://:PASSWORD@host:port, postgres://user:pass@host/db, or
// mongodb+srv://user:pass@cluster. We mask the userinfo (the part before
// '@') because that's where the secret lives; the scheme + host stay so
// the human still sees what the credential is FOR.
var connURIRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^@\s/]+@`)

// kvSecretRe matches inline key=value / key: value secrets such as
// password=hunter2, token: ghp_xxx, api_key="…". Case-insensitive on the
// key; masks the value up to the next whitespace, comma, or quote.
var kvSecretRe = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|auth|bearer)\b(\s*[:=]\s*)("?)[^\s",;]+`)

// RedactSecrets masks credential material in a free-text string so it
// never lands in an inbox title/body broadcast to every MANAGER in the
// workspace. The source-of-truth row (escalations / credentials table)
// still holds the real value behind its own access control; this is the
// projection, and the projection must not leak.
//
// Provider-aware first: delegates to the vetted lookout.Redact, which
// masks Anthropic / AWS (AKIA…, only 20 chars) / GitHub PAT / bearer
// tokens and labelled key=value secrets — coverage a local regex set
// would keep drifting behind. We then layer connection-string userinfo
// (redis://:pass@…, postgres://u:p@…) which lookout doesn't target.
//
// Idempotent and safe on already-clean text (returns it unchanged).
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	out, _ := lookout.Redact(s)
	out = connURIRe.ReplaceAllString(out, "$1••••@")
	out = kvSecretRe.ReplaceAllString(out, "$1$2$3••••")
	return out
}

// titleStripRe removes leading markdown heading / list / emphasis markers
// from a line so a prompt that starts with "## Change Plan" or "**Do X**"
// yields a clean "Change Plan" / "Do X" title.
var titleStripRe = regexp.MustCompile(`^[#>\-*\s]+`)
var wsCollapseRe = regexp.MustCompile(`\s+`)

// CleanTitle derives a single tidy title line from a (possibly multi-line,
// possibly markdown) body. It takes the first non-empty line, strips
// leading markdown markers, collapses internal whitespace, and truncates
// to max runes with an ellipsis. Falls back to the given default when the
// body is empty.
//
// This fixes inbox titles that were literally `body[:77]` — which dragged
// "\n\n## Change Plan…" newlines and heading hashes straight into the
// list row. A title is a label, not a body excerpt.
func CleanTitle(body string, max int, fallback string) string {
	var first string
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			first = t
			break
		}
	}
	if first == "" {
		return fallback
	}
	first = titleStripRe.ReplaceAllString(first, "")
	first = wsCollapseRe.ReplaceAllString(first, " ")
	first = strings.TrimSpace(first)
	if first == "" {
		return fallback
	}
	if max > 0 {
		r := []rune(first)
		if len(r) > max {
			cut := max - 1
			if cut < 1 {
				cut = 1
			}
			first = strings.TrimSpace(string(r[:cut])) + "…"
		}
	}
	return first
}
