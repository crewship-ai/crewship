package usermodel

import (
	"strings"
	"testing"
)

// The assertion that matters for #1669 is not that the prompt contains
// the word "stated". A test like that passes while the extractor writes
// inferences all day, and that is the entire risk of this feature.
//
// So: feed a REAL transcript, feed the candidate facts a model plausibly
// proposes from it — including the ones it should not have — and assert
// which are written and which are refused, by reason.

// transcript is the conversation every case below is verified against.
// Two operators speak in it, because a third party's statement about the
// subject is one of the things that has to be refused (Honcho's assent
// clause) and it cannot be tested with a one-human transcript.
func transcript() []Turn {
	return []Turn{
		{Role: "user", BySubject: true, Content: "Before we start — I run the platform team here, and the deploy pipeline is mine."},
		{Role: "assistant", Content: "Understood. I'll route pipeline questions to you."},
		{Role: "user", BySubject: true, Content: "One rule: commits must not carry a co-author trailer. Ever. That's a hard constraint from me."},
		{Role: "assistant", Content: "Noted. I take it you'd also prefer short answers?"},
		{Role: "user", BySubject: true, Content: "Yes, keep it short."},
		{Role: "assistant", Content: "You seem pretty frustrated with the review tooling today."},
		{Role: "user", BySubject: true, Content: "The CodeRabbit queue is 45 minutes deep again."},
		{Role: "user", BySubject: false, Content: "Pavel is basically the only person who understands the billing service."},
		{Role: "assistant", Content: "I'll keep that in mind."},
	}
}

func TestVerify_WritesWhatWasStatedAndRefusesTheRest(t *testing.T) {
	p := ProfileStatedTechnical
	turns := transcript()

	cases := []struct {
		name string
		cand Candidate
		// wantReason is "" when the candidate must be WRITTEN.
		wantReason string
	}{
		{
			name: "role stated in the subject's own words",
			cand: Candidate{
				Key:    "role",
				Value:  "runs the platform team",
				Quote:  "I run the platform team here",
				Source: "stated",
			},
		},
		{
			name: "ownership stated in the subject's own words",
			cand: Candidate{
				Key:    "owns",
				Value:  "the deploy pipeline",
				Quote:  "the deploy pipeline is mine",
				Source: "stated",
			},
		},
		{
			name: "the standing constraint the person actually declared",
			cand: Candidate{
				Key:    "constraint",
				Value:  "commits carry no co-author trailer",
				Quote:  "commits must not carry a co-author trailer",
				Source: "stated",
			},
		},

		// ── the refusals ────────────────────────────────────────────

		{
			name: "sentiment the assistant projected onto the person",
			cand: Candidate{
				Key:    "mood",
				Value:  "frustrated with the review tooling",
				Quote:  "You seem pretty frustrated with the review tooling today.",
				Source: "stated",
			},
			// There is no key for a mood, and that is checked before the
			// value is ever read.
			wantReason: ReasonUnknownKey,
		},
		{
			name: "sentiment smuggled in under an admissible key",
			cand: Candidate{
				Key:    "prefers",
				Value:  "is frustrated by slow review tooling",
				Quote:  "You seem pretty frustrated with the review tooling today.",
				Source: "stated",
			},
			// The span is real, but the assistant wrote it.
			wantReason: ReasonAssistantOrigin,
		},
		{
			name: "an inference the model declared honestly",
			cand: Candidate{
				Key:    "prefers",
				Value:  "deep technical detail",
				Quote:  "The CodeRabbit queue is 45 minutes deep again.",
				Source: "inferred",
			},
			wantReason: ReasonSourceNotAdmissible,
		},
		{
			name: "an inference the model did not declare, with no evidence for it",
			cand: Candidate{
				Key:    "prefers",
				Value:  "asynchronous review over synchronous",
				Quote:  "",
				Source: "stated",
			},
			wantReason: ReasonNoQuote,
		},
		{
			name: "model-authored prose substituted for a real span",
			cand: Candidate{
				Key:    "constraint",
				Value:  "no co-author trailers on commits",
				Quote:  "The user requires that commits never carry a co-author trailer.",
				Source: "stated",
			},
			wantReason: ReasonQuoteNotFound,
		},
		{
			name: "a third party's claim about the subject, without their assent",
			cand: Candidate{
				Key:    "owns",
				Value:  "the billing service",
				Quote:  "Pavel is basically the only person who understands the billing service.",
				Source: "stated",
			},
			wantReason: ReasonThirdParty,
		},
		{
			name: "a trend manufactured from one occurrence",
			cand: Candidate{
				Key:    "prefers",
				Value:  "always wants replies kept short",
				Quote:  "Yes, keep it short.",
				Source: "stated",
			},
			wantReason: ReasonTrendFromSingle,
		},
		{
			name: "the same preference written declaratively",
			cand: Candidate{
				Key:    "prefers",
				Value:  "short answers",
				Quote:  "Yes, keep it short.",
				Source: "stated",
			},
		},
		{
			name: "imperative phrasing, which a later session reads as an order",
			cand: Candidate{
				Key:    "prefers",
				Value:  "Always respond concisely",
				Quote:  "Yes, keep it short.",
				Source: "stated",
			},
			wantReason: ReasonImperative,
		},
		{
			name: "a quote too short to support anything",
			cand: Candidate{
				Key:    "role",
				Value:  "runs everything",
				Quote:  "I run",
				Source: "stated",
			},
			wantReason: ReasonQuoteTooShort,
		},
		{
			name: "a value longer than one field's budget",
			cand: Candidate{
				Key:    "constraint",
				Value:  "commits carry no co-author trailer " + strings.Repeat("and no generated-by footer ", 12),
				Quote:  "commits must not carry a co-author trailer",
				Source: "stated",
			},
			wantReason: ReasonValueTooLong,
		},
		{
			name: "an empty value",
			cand: Candidate{
				Key:    "role",
				Value:  "   ",
				Quote:  "I run the platform team here",
				Source: "stated",
			},
			wantReason: ReasonEmptyValue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accepted, refused := Verify(p, turns, []Candidate{tc.cand})
			if tc.wantReason == "" {
				if len(accepted) != 1 {
					t.Fatalf("expected the fact to be written, got accepted=%v refused=%v", accepted, refused)
				}
				if accepted[0].Key != tc.cand.Key || accepted[0].Value != strings.TrimSpace(tc.cand.Value) {
					t.Fatalf("wrote %q: %q, want %q: %q",
						accepted[0].Key, accepted[0].Value, tc.cand.Key, strings.TrimSpace(tc.cand.Value))
				}
				return
			}
			if len(accepted) != 0 {
				t.Fatalf("expected refusal %q, but the fact was WRITTEN as %q: %q",
					tc.wantReason, accepted[0].Key, accepted[0].Value)
			}
			if len(refused) != 1 {
				t.Fatalf("expected exactly one refusal, got %v", refused)
			}
			if refused[0].Reason != tc.wantReason {
				t.Fatalf("refused for %q, want %q", refused[0].Reason, tc.wantReason)
			}
		})
	}
}

// Whitespace is the one thing a model reliably fails to reproduce, and
// it cannot change which words were said — so the span match collapses
// runs of whitespace and is byte-exact otherwise. Case is NOT folded:
// the model picks a span, it does not restate one.
func TestVerify_QuoteMatchIsWhitespaceInsensitiveAndNothingElse(t *testing.T) {
	turns := []Turn{{
		Role:      "user",
		BySubject: true,
		Content:   "I prefer\n  short answers,\tand no filler.",
	}}
	base := Candidate{Key: "prefers", Value: "short answers", Source: "stated"}

	t.Run("whitespace differs", func(t *testing.T) {
		c := base
		c.Quote = "I prefer short answers, and no filler."
		if acc, ref := Verify(ProfileStatedTechnical, turns, []Candidate{c}); len(acc) != 1 {
			t.Fatalf("whitespace-normalised span should match; refused %v", ref)
		}
	})
	t.Run("a word differs", func(t *testing.T) {
		c := base
		c.Quote = "I prefer brief answers, and no filler."
		acc, ref := Verify(ProfileStatedTechnical, turns, []Candidate{c})
		if len(acc) != 0 {
			t.Fatalf("a reworded span must not match, got %v", acc)
		}
		if len(ref) != 1 || ref[0].Reason != ReasonQuoteNotFound {
			t.Fatalf("want %s, got %v", ReasonQuoteNotFound, ref)
		}
	})
	t.Run("case differs", func(t *testing.T) {
		c := base
		c.Quote = "i prefer short answers, and no filler."
		acc, _ := Verify(ProfileStatedTechnical, turns, []Candidate{c})
		if len(acc) != 0 {
			t.Fatalf("case-folded span must not match, got %v", acc)
		}
	})
}

// The mode is switched by what is bound, not by how the prompt is
// worded. ProfileOff writes nothing at all, even for a candidate that
// would sail through the shipped profile.
func TestVerify_ProfileOffAdmitsNothing(t *testing.T) {
	c := Candidate{
		Key:    "role",
		Value:  "runs the platform team",
		Quote:  "I run the platform team here",
		Source: "stated",
	}
	acc, ref := Verify(ProfileOff, transcript(), []Candidate{c})
	if len(acc) != 0 {
		t.Fatalf("ProfileOff wrote %v", acc)
	}
	if len(ref) != 1 || ref[0].Reason != ReasonProfileWritesNothing {
		t.Fatalf("want %s, got %v", ReasonProfileWritesNothing, ref)
	}
}

// Duplicate keys in one extraction must not produce two bullets for one
// field — the on-disk format is one line per key and the merge is keyed
// on it, so a second value for the same key is a silent overwrite at
// best. First wins; the loser is recorded rather than dropped silently.
func TestVerify_SecondValueForOneKeyIsRefused(t *testing.T) {
	turns := transcript()
	acc, ref := Verify(ProfileStatedTechnical, turns, []Candidate{
		{Key: "role", Value: "runs the platform team", Quote: "I run the platform team here", Source: "stated"},
		{Key: "role", Value: "owns the deploy pipeline", Quote: "the deploy pipeline is mine", Source: "stated"},
	})
	if len(acc) != 1 || acc[0].Value != "runs the platform team" {
		t.Fatalf("first value for a key should win, got %v", acc)
	}
	if len(ref) != 1 || ref[0].Reason != ReasonDuplicateKey {
		t.Fatalf("want %s, got %v", ReasonDuplicateKey, ref)
	}
}

// Returning nothing is a success, not a failure — Veracium's "Only
// extract what THIS event states. Empty lists are valid."
func TestVerify_EmptyCandidateSetIsNotAnError(t *testing.T) {
	acc, ref := Verify(ProfileStatedTechnical, transcript(), nil)
	if len(acc) != 0 || len(ref) != 0 {
		t.Fatalf("empty in, empty out; got acc=%v ref=%v", acc, ref)
	}
}
