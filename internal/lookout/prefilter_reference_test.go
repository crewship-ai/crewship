package lookout

import (
	"fmt"
	"unicode/utf8"
)

func legacyPrefilterScanInput(text string) ScanResult {
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
