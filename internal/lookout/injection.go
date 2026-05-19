package lookout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"
)

// injectionRule pairs a compiled regex with the metadata used to build a
// Finding. Compiled once at package init so hot-path scans never pay the
// regex compilation cost.
type injectionRule struct {
	pattern  *regexp.Regexp
	kind     Kind
	severity Severity
	detail   string
}

var injectionRules []injectionRule

func init() {
	patterns := []struct {
		expr     string
		kind     Kind
		severity Severity
		detail   string
	}{
		// Role override
		{`(?i)ignore (previous|all|above) (instructions|prompts)`, KindRoleOverride, SeverityHigh, "instruction-override request"},
		{`(?i)disregard .{0,40}? instructions`, KindRoleOverride, SeverityHigh, "disregard-instructions request"},
		{`(?i)you are (now|actually) (a |an )?`, KindRoleOverride, SeverityMedium, "role-redefinition attempt"},
		{`(?i)new (instructions|rules|persona)`, KindRoleOverride, SeverityMedium, "new-persona injection"},
		// System prompt leak
		{`(?i)(reveal|show|print|output) (your|the) (system prompt|instructions|rules)`, KindSystemPromptLeak, SeverityHigh, "request to reveal system prompt"},
		{`(?i)what (is|are) your (system|initial) (prompt|instructions)`, KindSystemPromptLeak, SeverityMedium, "system-prompt probing question"},
		// Jailbreak tropes
		{`(?i)DAN (mode|prompt)`, KindJailbreak, SeverityHigh, "DAN jailbreak"},
		{`(?i)developer mode`, KindJailbreak, SeverityMedium, "developer-mode jailbreak"},
		{`(?i)jailbreak`, KindJailbreak, SeverityMedium, "explicit jailbreak mention"},
		{`(?i)pretend you (have no|are without) restrictions`, KindJailbreak, SeverityHigh, "restriction-removal jailbreak"},
	}
	injectionRules = make([]injectionRule, 0, len(patterns))
	for _, p := range patterns {
		injectionRules = append(injectionRules, injectionRule{
			pattern:  regexp.MustCompile(p.expr),
			kind:     p.kind,
			severity: p.severity,
			detail:   p.detail,
		})
	}
}

// confusable unicode codepoints we treat as suspicious in agent input.
const (
	zwsp     = '\u200B' // zero-width space
	zwnj     = '\u200C' // zero-width non-joiner
	zwj      = '\u200D' // zero-width joiner
	bom      = '\uFEFF' // byte-order mark / zero-width no-break space
	rtlOverr = '\u202E' // right-to-left override
)

// rtlOverrideThreshold is the per-1024-rune density above which RTL override
// presence is escalated from low to high severity. A single RTL override in
// a multilingual document is normal; many in a short string is the classic
// filename-spoofing trick.
const rtlOverrideThreshold = 1

// ScanInput runs the heuristic injection rules over text and returns a
// ScanResult. Verdict is Block if any high/critical finding fires, Allow
// otherwise. The function is pure and safe to call concurrently.
func ScanInput(text string) ScanResult {
	res := ScanResult{Findings: []Finding{}, Verdict: VerdictAllow}
	if text == "" {
		return res
	}
	for _, r := range injectionRules {
		if loc := r.pattern.FindStringIndex(text); loc != nil {
			match := text[loc[0]:loc[1]]
			res.Findings = append(res.Findings, Finding{
				Kind:     r.kind,
				Severity: r.severity,
				Detail:   r.detail,
				Matched:  truncate(match, 80),
				Position: loc[0],
				MatchEnd: loc[1],
			})
		}
	}

	// Emit one Finding per zero-width occurrence so SANITIZE can redact
	// every single one, not just the first. An earlier version emitted
	// a single Finding for the first ZW rune which left every subsequent
	// ZWSP/ZWNJ/ZWJ/BOM in the source text untouched after sanitize —
	// an adversary could chain ZWSP + ZWJ and slip the ZWJ past the
	// homoglyph-joining defense. We keep the severity at "high" for the
	// first occurrence (drives the block verdict) and "low" for the
	// rest (so the block decision in soft modes isn't influenced by
	// rune count, but sanitize still walks every span). Block-mode
	// callers don't care about the per-rune findings because they fail
	// closed regardless.
	zwSeen := false
	rtlSpans := make([]int, 0, 4) // start byte offsets of RTL override runes
	rtlCount := 0
	for i, r := range text {
		switch r {
		case zwsp, zwnj, zwj, bom:
			sev := SeverityLow
			if !zwSeen {
				sev = SeverityHigh
				zwSeen = true
			}
			res.Findings = append(res.Findings, Finding{
				Kind:     KindZeroWidth,
				Severity: sev,
				Detail:   fmt.Sprintf("zero-width unicode codepoint U+%04X present", r),
				Matched:  fmt.Sprintf("U+%04X", r),
				Position: i,
				MatchEnd: i + utf8.RuneLen(r),
			})
		case rtlOverr:
			rtlSpans = append(rtlSpans, i)
			rtlCount++
		}
	}
	if rtlCount > 0 {
		sev := SeverityLow
		if rtlCount >= rtlOverrideThreshold && len(text) < 1024 {
			sev = SeverityHigh
		}
		// Emit one Finding per RTL override occurrence so sanitize
		// covers every span. Same reasoning as the zero-width loop —
		// a single Finding for the FIRST occurrence let later RTL
		// overrides survive sanitize.
		for idx, pos := range rtlSpans {
			perSev := SeverityLow
			if idx == 0 {
				perSev = sev
			}
			res.Findings = append(res.Findings, Finding{
				Kind:     KindRTLOverride,
				Severity: perSev,
				Detail:   fmt.Sprintf("right-to-left override present (%d occurrences)", rtlCount),
				Matched:  "U+202E",
				Position: pos,
				MatchEnd: pos + utf8.RuneLen(rtlOverr),
			})
		}
	}

	for _, f := range res.Findings {
		if f.Severity == SeverityHigh || f.Severity == SeverityCritical {
			res.Verdict = VerdictBlock
			break
		}
	}
	return res
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	if n > len(runes) {
		n = len(runes)
	}
	return string(runes[:n]) + "…"
}

// LakeraScanner is an optional remote-detection upgrade that consults the
// Lakera Guard API in addition to the local heuristics. Construct with
// WithLakeraAPIKey; call ScanInput on the returned scanner. The local
// rules always run first; the remote call is only made when local rules
// did not already produce a Block verdict, to keep latency and cost down.
type LakeraScanner struct {
	apiKey   string
	endpoint string
	client   *http.Client
}

// DefaultLakeraEndpoint is the production Lakera Guard v2 URL. Exposed
// so callers can point at a test fixture or regional deployment via
// WithEndpoint without hardcoding the string twice.
const DefaultLakeraEndpoint = "https://api.lakera.ai/v2/guard"

// WithLakeraAPIKey returns a scanner that augments local detection with
// Lakera's hosted classifier. Pass an empty key to disable the remote call
// entirely (the scanner then behaves identically to ScanInput). The
// endpoint defaults to DefaultLakeraEndpoint and can be overridden via
// WithEndpoint for testing (httptest.Server) or alternative hosts.
func WithLakeraAPIKey(key string) *LakeraScanner {
	return &LakeraScanner{
		apiKey:   key,
		endpoint: DefaultLakeraEndpoint,
		client:   &http.Client{Timeout: 3 * time.Second},
	}
}

// WithEndpoint overrides the Lakera endpoint. Returns the receiver so
// the config chain reads naturally: WithLakeraAPIKey(k).WithEndpoint(url).
func (l *LakeraScanner) WithEndpoint(url string) *LakeraScanner {
	if url != "" {
		l.endpoint = url
	}
	return l
}

// ScanInput is the LakeraScanner equivalent of the package-level ScanInput.
// It always runs the local rules; on Allow it additionally calls Lakera and
// merges any flag the remote returns. Network errors are swallowed (logged
// by the caller via the returned findings being unchanged) — the local
// result is authoritative on failure.
func (l *LakeraScanner) ScanInput(ctx context.Context, text string) ScanResult {
	local := ScanInput(text)
	if l == nil || l.apiKey == "" || local.Verdict == VerdictBlock {
		return local
	}
	flagged, err := l.callLakera(ctx, text)
	if err != nil || !flagged {
		return local
	}
	local.Findings = append(local.Findings, Finding{
		Kind:     KindLakeraDetected,
		Severity: SeverityHigh,
		Detail:   "Lakera Guard flagged the input",
		Position: -1,
	})
	local.Verdict = VerdictBlock
	return local
}

type lakeraReq struct {
	Input string `json:"input"`
}

type lakeraResp struct {
	Results []struct {
		Flagged bool `json:"flagged"`
	} `json:"results"`
}

func (l *LakeraScanner) callLakera(ctx context.Context, text string) (bool, error) {
	body, _ := json.Marshal(lakeraReq{Input: text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+l.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	if resp.StatusCode/100 != 2 {
		return false, fmt.Errorf("lakera: status %d", resp.StatusCode)
	}
	var parsed lakeraResp
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return false, err
	}
	for _, r := range parsed.Results {
		if r.Flagged {
			return true, nil
		}
	}
	return false, nil
}
