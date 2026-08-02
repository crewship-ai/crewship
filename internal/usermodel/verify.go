package usermodel

import (
	"strings"
	"unicode"
)

// Turn is one message of a transcript, already attributed.
type Turn struct {
	// Role is llm.RoleUser / llm.RoleAssistant as recorded on the
	// conversation_messages row.
	Role string

	// BySubject is true only when this turn was authored by the person
	// the model is about. False for every agent turn, and for a human
	// turn written by somebody else in a group chat.
	//
	// It is the assent clause made structural (Honcho): a third party's
	// statement about the subject is evidence about the SPEAKER, not
	// about the subject, and counts only if the subject assented — which,
	// if they did, they did in a turn of their own that can be quoted
	// instead.
	BySubject bool

	Content string
}

// Candidate is one fact as the model proposed it, before verification.
// The JSON tags are the wire shape the extraction prompt asks for.
type Candidate struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Quote is the verbatim span from the transcript that states the
	// fact. Required: it is what makes the claim checkable, and a
	// candidate without one is refused rather than trusted.
	Quote  string `json:"quote"`
	Source string `json:"source"`
}

// Fact is a verified candidate — a value the profile admits, backed by a
// span found in the subject's own words.
type Fact struct {
	Key   string
	Value string
	Quote string
}

// Refusal records a candidate that was not written, and why. Refusals
// are returned rather than dropped so the sweep can count them: an
// extractor that refuses everything and an extractor that is not running
// look identical from the outside otherwise.
type Refusal struct {
	Key    string
	Value  string
	Reason string
}

// Refusal reasons. Stable strings — they are logged, and the docs name
// them.
const (
	// ReasonProfileWritesNothing — the configured profile admits no
	// source type or has no fields ("off").
	ReasonProfileWritesNothing = "profile_writes_nothing"

	// ReasonUnknownKey — no such field in the profile. This is the
	// checkable form of "technical, not emotionally coloured": there is
	// no key a mood can be written to, so it is refused before its value
	// is read.
	ReasonUnknownKey = "key_not_in_profile"

	// ReasonDuplicateKey — a second value for a field already taken in
	// this extraction. The on-disk format is one bullet per key.
	ReasonDuplicateKey = "duplicate_key"

	// ReasonSourceNotAdmissible — the model declared a source type this
	// profile does not write (an honestly-labelled inference).
	ReasonSourceNotAdmissible = "source_not_admissible"

	ReasonEmptyValue = "empty_value"

	// ReasonValueTooLong — over the profile's per-field budget. Refused
	// rather than truncated: half a stated fact is not a stated fact.
	ReasonValueTooLong = "value_over_field_cap"

	// ReasonNoQuote — no evidence span at all. The undeclared inference.
	ReasonNoQuote = "no_evidence_quote"

	// ReasonQuoteTooShort — a span too small to support a claim. Without
	// a floor the model quotes "I" and hangs anything off it.
	ReasonQuoteTooShort = "evidence_quote_too_short"

	// ReasonQuoteNotFound — the span is nowhere in the transcript. This
	// is model-authored prose standing in for evidence.
	ReasonQuoteNotFound = "quote_not_in_transcript"

	// ReasonAssistantOrigin — the span is real but the ASSISTANT said
	// it. The agent's own words are not evidence about the human (memU
	// v1.5.1's exclusion), and it is the route by which a projected
	// sentiment gets laundered into a fact.
	ReasonAssistantOrigin = "quote_from_an_assistant_turn"

	// ReasonThirdParty — the span is real and human, but a DIFFERENT
	// human said it. See Turn.BySubject.
	ReasonThirdParty = "quote_not_from_the_subject"

	// ReasonImperative — the value is phrased as an order. Imperative
	// phrasing is re-read as a directive in a later session and can
	// override what the person is asking for right now.
	ReasonImperative = "imperative_phrasing"

	// ReasonTrendFromSingle — the value claims a habit the evidence does
	// not. Graphiti's rule: a single mention can support a fact, but not
	// a trend, habit or preference unless the text says so directly.
	ReasonTrendFromSingle = "trend_language_not_in_evidence"
)

// Verify is the structural gate between what the model proposed and what
// is written to disk.
//
// It is the whole guarantee of this package. The prompt asks for stated
// facts; this decides whether it got them. Candidates are processed in
// order and the FIRST failing check decides the refusal reason, so the
// reason names the most fundamental thing wrong rather than an incidental
// one.
//
// Returning nothing is a success. An empty candidate list yields two
// empty slices and no error anywhere up the stack.
func Verify(p Profile, turns []Turn, cands []Candidate) (accepted []Fact, refused []Refusal) {
	if len(cands) == 0 {
		return nil, nil
	}
	if !p.Writes() {
		for _, c := range cands {
			refused = append(refused, Refusal{c.Key, c.Value, ReasonProfileWritesNothing})
		}
		return nil, refused
	}

	// Pre-normalise the transcript once: two haystacks, one of what the
	// subject said and one of everything else, so "the span is real but
	// the wrong person said it" is distinguishable from "the span is not
	// real at all". The distinction is not cosmetic — it is the
	// difference between a model that is quoting and a model that is
	// writing.
	var subject, others []string
	for _, t := range turns {
		n := collapseSpaces(t.Content)
		if n == "" {
			continue
		}
		if t.BySubject && strings.EqualFold(t.Role, "user") {
			subject = append(subject, n)
			continue
		}
		others = append(others, n)
	}

	taken := make(map[string]bool, len(cands))
	for _, c := range cands {
		key := strings.ToLower(strings.TrimSpace(c.Key))
		val := strings.TrimSpace(c.Value)
		quote := strings.TrimSpace(c.Quote)

		reason := ""
		switch {
		case key == "":
			reason = ReasonUnknownKey
		default:
			if _, ok := p.hasKey(key); !ok {
				reason = ReasonUnknownKey
			}
		}
		if reason == "" && taken[key] {
			reason = ReasonDuplicateKey
		}
		if reason == "" && !p.admits(SourceType(strings.ToLower(strings.TrimSpace(c.Source)))) {
			reason = ReasonSourceNotAdmissible
		}
		if reason == "" && val == "" {
			reason = ReasonEmptyValue
		}
		if reason == "" && len([]rune(val)) > p.MaxValueChars {
			reason = ReasonValueTooLong
		}
		if reason == "" && quote == "" {
			reason = ReasonNoQuote
		}
		if reason == "" && len([]rune(quote)) < p.MinQuoteChars {
			reason = ReasonQuoteTooShort
		}
		if reason == "" {
			needle := collapseSpaces(quote)
			switch {
			case containsSpan(subject, needle):
				// Stated by the subject — the only admissible origin.
			case containsSpan(others, needle):
				reason = originRefusal(turns, needle)
			default:
				reason = ReasonQuoteNotFound
			}
		}
		if reason == "" && isImperative(val) {
			reason = ReasonImperative
		}
		if reason == "" && hasTrendLanguage(val) && !hasTrendLanguage(quote) {
			reason = ReasonTrendFromSingle
		}

		if reason != "" {
			refused = append(refused, Refusal{c.Key, c.Value, reason})
			continue
		}
		taken[key] = true
		accepted = append(accepted, Fact{Key: key, Value: val, Quote: quote})
	}
	return accepted, refused
}

// originRefusal decides which "wrong speaker" reason applies to a span
// already known to be in the transcript but not in the subject's turns.
func originRefusal(turns []Turn, needle string) string {
	for _, t := range turns {
		if strings.EqualFold(t.Role, "user") && !t.BySubject &&
			strings.Contains(collapseSpaces(t.Content), needle) {
			return ReasonThirdParty
		}
	}
	return ReasonAssistantOrigin
}

// containsSpan reports whether needle appears in any haystack entry.
//
// The comparison is byte-exact except that runs of whitespace on both
// sides are collapsed to one space. That is the narrowest relaxation
// that survives a model reflowing a quote, and it cannot change which
// words were said. Case is NOT folded and no other normalisation is
// applied: the model's job is to point at a span, not to restate one.
//
// This is OpenClaw's mechanism — consolidation there enforces byte
// equality so the model picks which span and which lifecycle action and
// cannot author replacement prose. It is the strongest guarantee in the
// field survey and the reason it is strong is that it is not a prompt.
func containsSpan(haystacks []string, needle string) bool {
	if needle == "" {
		return false
	}
	for _, h := range haystacks {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// collapseSpaces reduces every run of Unicode whitespace to a single
// space and trims the ends.
func collapseSpaces(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// imperativeOpeners are the sentence starts that turn a recorded fact
// into an order the next session obeys. Matched at the START of the
// value only: "prefers short answers" is a fact, "Always respond
// concisely" is an instruction, and the difference is where the verb sits.
var imperativeOpeners = []string{
	"please ", "must ", "should ",
	"do ", "don't ", "do not ", "avoid ", "ensure ", "make sure",
	"remember to", "use ", "write ", "keep ", "prioritise ", "prioritize ",
	"respond ", "reply ", "answer ", "you must", "you should",
}

// isImperative reports whether a value reads as a directive rather than
// a description.
//
// A leading frequency adverb is stripped before the check, because it
// modifies whatever follows rather than deciding the mood: "always
// respond concisely" is an order, "always wants replies kept short" is a
// description (of a claim the trend check then has to find evidence for).
// Treating a bare "always" as imperative would collapse those two into
// one reason and report the wrong thing about the more interesting case.
func isImperative(v string) bool {
	l := strings.ToLower(strings.TrimSpace(v))
	for _, m := range trendMarkers {
		if strings.HasPrefix(l, m+" ") {
			l = strings.TrimSpace(strings.TrimPrefix(l, m+" "))
			break
		}
	}
	for _, o := range imperativeOpeners {
		if strings.HasPrefix(l, o) {
			return true
		}
	}
	return false
}

// trendMarkers are the words that turn one occurrence into a habit.
var trendMarkers = []string{
	"always", "never", "usually", "typically", "generally", "normally",
	"often", "habitually", "consistently", "every time", "in general",
	"as a rule", "tends to", "tend to",
}

// hasTrendLanguage reports whether a string claims a pattern.
//
// Used as a comparison, not a ban: a value may claim a habit only when
// the evidence claims one too. "always wants replies short" backed by
// "keep it short" is manufactured; backed by "I always want replies
// short" it is quoted.
func hasTrendLanguage(s string) bool {
	l := " " + collapseSpaces(strings.ToLower(s)) + " "
	for _, m := range trendMarkers {
		if strings.Contains(l, " "+m+" ") || strings.Contains(l, " "+m+",") ||
			strings.Contains(l, " "+m+".") {
			return true
		}
	}
	return false
}
