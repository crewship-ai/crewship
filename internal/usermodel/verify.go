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
	// a floor the model quotes "I" and hangs anything off it. Under a
	// profile with AllowShortCompleteSentence this means the span is a
	// FRAGMENT: shorter than the floor and not a sentence the subject
	// finished.
	ReasonQuoteTooShort = "evidence_quote_too_short"

	// ReasonQuoteStatesNothing — the span is a complete sentence the
	// subject wrote, and short, but it states nothing on its own: "Yes.",
	// "Ok, do it.". Its content lives in the AGENT's question, so
	// accepting it would let the assistant's words in under the subject's
	// name — the exact route ReasonAssistantOrigin closes. Separated from
	// ReasonQuoteTooShort so the sweep's refusal histogram distinguishes
	// "the model quoted a fragment" from "the model quoted an assent"
	// (#1700); they call for opposite fixes.
	ReasonQuoteStatesNothing = "evidence_quote_states_nothing"

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
	// subjectSentences is the same text cut at sentence boundaries, used
	// only by the short-answer exemption below. Built from the subject's
	// turns alone, so the exemption can never readmit an agent's or a
	// third party's short sentence.
	var subjectSentences []string
	for _, t := range turns {
		n := collapseSpaces(t.Content)
		if n == "" {
			continue
		}
		if t.BySubject && strings.EqualFold(t.Role, "user") {
			subject = append(subject, n)
			if p.AllowShortCompleteSentence {
				subjectSentences = append(subjectSentences, sentences(n)...)
			}
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
		if _, ok := p.hasKey(key); !ok {
			// Covers the empty key too: "" is in no profile.
			reason = ReasonUnknownKey
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
			// Below the floor. That is the right answer for a fragment
			// and the wrong one for an answer to a direct question —
			// see Profile.AllowShortCompleteSentence and #1700.
			switch {
			case !p.AllowShortCompleteSentence:
				reason = ReasonQuoteTooShort
			case !isCompleteSentence(subjectSentences, quote):
				reason = ReasonQuoteTooShort
			case !statesSomething(quote):
				reason = ReasonQuoteStatesNothing
			}
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

// sentenceEnders terminate a sentence. Deliberately short: this is a cut
// for "did the person finish the thought", not a linguistic parse.
const sentenceEnders = ".!?…"

// sentences cuts one whitespace-collapsed turn into its sentences,
// terminator included ("UTC+1. Ping me on Slack." → ["UTC+1.", "Ping me
// on Slack."]). The final span is kept even when the person did not
// punctuate it, because a turn that ends is a sentence that ended.
func sentences(turn string) []string {
	var out []string
	start := 0
	rs := []rune(turn)
	for i := 0; i < len(rs); i++ {
		if !strings.ContainsRune(sentenceEnders, rs[i]) {
			continue
		}
		// Swallow a run of terminators ("?!", "...") into one boundary.
		j := i
		for j+1 < len(rs) && strings.ContainsRune(sentenceEnders, rs[j+1]) {
			j++
		}
		if s := strings.TrimSpace(string(rs[start : j+1])); s != "" {
			out = append(out, s)
		}
		i = j
		start = j + 1
	}
	if s := strings.TrimSpace(string(rs[start:])); s != "" {
		out = append(out, s)
	}
	return out
}

// isCompleteSentence reports whether the quote IS one of the subject's
// sentences — not merely contained in one.
//
// That is the whole distinction the exemption rests on. "UTC+1." is a
// sentence the person finished; "I run" and "the day." are cuts out of
// one, and a cut is what the character floor exists to refuse, because
// the words on either side of it are the ones that decide what it meant.
//
// A missing terminal full stop is tolerated (models drop it constantly),
// on the same reasoning as the whitespace collapse in containsSpan: it
// cannot change which words were said. Nothing else is relaxed — case is
// not folded and no interior punctuation is normalised.
func isCompleteSentence(subjectSentences []string, quote string) bool {
	q := strings.TrimRight(collapseSpaces(quote), sentenceEnders)
	if q == "" {
		return false
	}
	for _, s := range subjectSentences {
		if strings.TrimRight(s, sentenceEnders) == q {
			return true
		}
	}
	return false
}

// statesSomething reports whether a span carries at least one word that
// says anything on its own.
//
// The test is a floor on CONTENT rather than on characters — the third
// option #1700 lists, applied only where the character floor has been
// relaxed. It is what stops the exemption from becoming the hole the
// floor was plugging: "Yes." and "Ok, do it." are complete sentences
// whose meaning is entirely in the question that preceded them, so
// admitting them would record the AGENT's proposal as the person's
// statement. "UTC+1.", "Weekdays." and "Czech." name the thing they are
// about and stand up without the question.
//
// Single characters do not count: a one-letter token is the "I"-class
// span the floor was written for.
func statesSomething(s string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		w := strings.TrimFunc(f, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len([]rune(w)) < 2 || contentlessWords[w] {
			continue
		}
		return true
	}
	return false
}

// contentlessWords are the words that state nothing on their own: bare
// assent and backchannel, pronouns, articles, auxiliaries, prepositions,
// conjunctions, and the frequency adverbs that only modify something
// else. A span made of nothing but these is a span whose meaning came
// from the other speaker.
//
// Over-listing is the safe direction: this set can only WITHHOLD the
// exemption, and a span it withholds from is refused exactly as it is
// today. Under-listing is what would let an assent through.
var contentlessWords = map[string]bool{
	// assent, dissent, backchannel
	"yes": true, "yeah": true, "yep": true, "yup": true, "aye": true,
	"no": true, "nope": true, "nah": true, "not": true,
	"ok": true, "okay": true, "sure": true, "right": true, "correct": true,
	"exactly": true, "agreed": true, "agree": true, "indeed": true,
	"absolutely": true, "definitely": true, "precisely": true, "true": true,
	"please": true, "thanks": true, "thank": true, "cheers": true,
	"fine": true, "good": true, "great": true, "cool": true, "perfect": true,
	"understood": true, "noted": true, "gotcha": true, "maybe": true,
	"perhaps": true, "hmm": true, "huh": true, "oh": true, "ah": true,
	"well": true, "yet": true, "still": true,
	// pronouns and determiners
	"me": true, "my": true, "mine": true, "we": true, "us": true,
	"our": true, "ours": true, "you": true, "your": true, "yours": true,
	"he": true, "him": true, "his": true, "she": true, "her": true,
	"hers": true, "it": true, "its": true, "they": true, "them": true,
	"their": true, "theirs": true, "the": true, "an": true, "this": true,
	"that": true, "these": true, "those": true, "there": true, "here": true,
	// auxiliaries, conjunctions, prepositions
	"is": true, "am": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "being": true, "do": true, "does": true,
	"did": true, "done": true, "have": true, "has": true, "had": true,
	"will": true, "would": true, "can": true, "could": true, "shall": true,
	"should": true, "may": true, "might": true, "must": true,
	"and": true, "or": true, "but": true, "if": true, "so": true,
	"then": true, "than": true, "as": true, "of": true, "to": true,
	"in": true, "on": true, "at": true, "by": true, "for": true,
	"with": true, "from": true, "into": true, "about": true,
	// degree and frequency adverbs — they modify, they do not state
	"too": true, "very": true, "just": true, "really": true, "quite": true,
	"more": true, "most": true, "less": true, "all": true, "any": true,
	"some": true, "none": true, "always": true, "never": true,
	"usually": true, "often": true, "sometimes": true, "typically": true,
	"generally": true, "normally": true,
	// contentless nouns
	"thing": true, "things": true, "stuff": true, "one": true,
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
