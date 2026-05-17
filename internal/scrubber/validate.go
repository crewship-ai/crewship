package scrubber

import "regexp"

// Mode selects the policy Validate applies when one or more credential
// patterns hit. Block rejects the input at the boundary; Warn lets it
// through so the caller can log; Redact returns the cleaned form (the
// existing Scrub behaviour) for callers that want the cleaned text plus
// the list of hits.
type Mode int

const (
	// ModeBlock rejects input that contains any credential. The
	// memory-write boundary uses this so a leaked key never lands on
	// disk in AGENT.md / CREW.md / pins.md.
	ModeBlock Mode = iota
	// ModeWarn allows the input through but reports the hits, so the
	// caller can log + alert without blocking writes — useful when
	// rolling out scrubber coverage on a noisy code path.
	ModeWarn
	// ModeRedact returns the cleaned string (with [REDACTED:name]
	// substitutions) and the hits list. Mirrors Scrub for callers
	// that want the structured Hit metadata alongside the cleaned
	// payload.
	ModeRedact
)

// Decision is what the caller should do with the input given Mode.
// Allow means "proceed with the input" (or with Cleaned in Redact mode).
// Reject means "do not persist or forward; surface the hits to the user".
type Decision int

const (
	DecisionAllow Decision = iota
	DecisionReject
)

// Hit is one credential pattern match against the normalised input.
// Pattern is the registered name (empty string for generic patterns —
// the same convention Scrub uses internally). Index/Length are byte
// offsets into the NORMALISED string (zero-width characters stripped)
// so callers comparing back to the raw input must do their own mapping.
type Hit struct {
	Pattern string
	Index   int
	Length  int
}

// Result is what Validate returns.
type Result struct {
	Decision Decision
	// Cleaned holds the redacted form of the input in ModeRedact; in
	// ModeBlock and ModeWarn it is the normalised input (zero-width
	// stripped) so callers writing audit logs can log a safe
	// representation without re-doing the strip.
	Cleaned string
	Hits    []Hit
}

// Validate inspects input against every registered pattern, returns a
// Decision based on mode, and includes the list of hits regardless of
// decision. The zero-width-strip normalisation Scrub uses applies here
// too so a key obfuscated with ZWSP is still detected.
//
// Memory writer + sidecar /memory/write are the primary callers; the
// scrubber itself stays free of any memory or HTTP coupling.
func (s *Scrubber) Validate(input string, mode Mode) Result {
	return s.ValidateWithAllowlist(input, mode, "")
}

// ValidateWithAllowlist is Validate with a per-call allowlist regex.
// Hits whose matched text fully matches the allowlist pattern are
// dropped from the result (e.g. operators document an
// `sk-ant-EXAMPLE_PLACEHOLDER` shape in their runbook; they pass the
// matching regex via workspace_settings.memory_config so docs aren't
// rejected).
//
// Fail-closed: an invalid allowlist regex is ignored — the function
// behaves as if no allowlist was supplied. This is deliberate: the
// alternative ("bypass scrubber when the regex doesn't parse") would
// turn a misconfiguration into a security regression.
func (s *Scrubber) ValidateWithAllowlist(input string, mode Mode, allowlist string) Result {
	if input == "" {
		return Result{Decision: DecisionAllow, Cleaned: ""}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	normalised := stripZeroWidth(input)

	var allowRe *regexp.Regexp
	if allowlist != "" {
		// Ignore compilation errors — fail-closed, see doc.
		allowRe, _ = regexp.Compile(allowlist)
	}

	var hits []Hit
	for _, p := range s.patterns {
		matches := p.re.FindAllStringIndex(normalised, -1)
		for _, m := range matches {
			matched := normalised[m[0]:m[1]]
			// Allowlist patterns must match the full matched span,
			// not a substring of it. MatchString would happily accept
			// a pattern like "test" as a license to write any string
			// containing the substring "test" — including
			// "sk-ant-test-..." which is exactly what the allowlist
			// is meant NOT to cover. Anchor on span boundaries instead.
			if allowRe != nil {
				if span := allowRe.FindStringIndex(matched); span != nil && span[0] == 0 && span[1] == len(matched) {
					continue
				}
			}
			hits = append(hits, Hit{
				Pattern: p.name,
				Index:   m[0],
				Length:  m[1] - m[0],
			})
		}
	}

	res := Result{Hits: hits}

	switch mode {
	case ModeRedact:
		// Reuse the existing Scrub path so the cleaned output stays
		// byte-identical with the public scrubber API. We release the
		// read lock first to avoid re-entrant lock acquisition.
		res.Decision = DecisionAllow
		s.mu.RUnlock()
		res.Cleaned = s.Scrub(input)
		s.mu.RLock()
	case ModeWarn:
		res.Decision = DecisionAllow
		res.Cleaned = normalised
	case ModeBlock:
		if len(hits) > 0 {
			res.Decision = DecisionReject
		} else {
			res.Decision = DecisionAllow
		}
		res.Cleaned = normalised
	default:
		// Unknown mode: fail-closed.
		if len(hits) > 0 {
			res.Decision = DecisionReject
		} else {
			res.Decision = DecisionAllow
		}
		res.Cleaned = normalised
	}

	return res
}
