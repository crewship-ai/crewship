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
			name: "an empty key",
			cand: Candidate{
				Key:    "  ",
				Value:  "runs the platform team",
				Quote:  "I run the platform team here",
				Source: "stated",
			},
			// "" is in no profile, so the closed-key-set check answers it
			// without a special case.
			wantReason: ReasonUnknownKey,
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

// #1700 — the transcript a real claude-haiku-4-5 was measured against
// under #1698. Across 33 extractions it proposed 57 candidates, 53 were
// stored, and all 4 refusals came from these two operator turns with the
// same reason: evidence_quote_too_short. Every one of them was a true
// fact the person had stated.
//
// Not one quote_not_in_transcript, key_not_in_profile, imperative_phrasing
// or trend_language_not_in_evidence in 57 candidates — so this was not one
// refusal among many, it was the ONLY refusal the gate produced, and it
// was wrong every time it fired.
func measuredTranscript() []Turn {
	return []Turn{
		{Role: "assistant", Content: "I can. What timezone are you in, and how should I reach you when it fires?"},
		{Role: "user", BySubject: true, Content: "UTC+1. Ping me on Slack, not email — I don't read email during the day."},
		{Role: "assistant", Content: "Slack it is. Do you want it daily or weekdays only?"},
		{Role: "user", BySubject: true, Content: "Weekdays."},
	}
}

// The answer to a direct question is the shape the character floor gets
// wrong: the information density of a real answer is inversely related to
// its length, and "UTC+1." is the most precise timezone statement a person
// can make. What distinguishes it from the "I" the floor exists to refuse
// is not length — it is that the person finished a sentence.
func TestVerify_ShortAnswerToADirectQuestionIsEvidence(t *testing.T) {
	p := ProfileStatedTechnical
	turns := measuredTranscript()

	cases := []struct {
		name       string
		cand       Candidate
		wantReason string // "" — must be WRITTEN
	}{
		{
			name: "the timezone answer, refused on all three measured runs",
			cand: Candidate{
				Key: "timezone", Value: "UTC+1",
				Quote: "UTC+1.", Source: "stated",
			},
		},
		{
			name: "the same answer quoted without its full stop",
			cand: Candidate{
				Key: "timezone", Value: "UTC+1",
				Quote: "UTC+1", Source: "stated",
			},
		},
		{
			name: "the one-word answer that is the whole turn",
			cand: Candidate{
				Key: "prefers", Value: "on-call notifications on weekdays only",
				Quote: "Weekdays.", Source: "stated",
			},
		},
		{
			// Stored on all three runs already — from the SAME sentence as
			// the timezone answer. The regression guard: the fix must not
			// disturb what the gate gets right.
			name: "the contact fact from the same sentence, already stored",
			cand: Candidate{
				Key: "contact", Value: "reach via Slack, not email",
				Quote: "Ping me on Slack, not email", Source: "stated",
			},
		},
		{
			// The floor's real target, and it must stay refused: a span cut
			// out of the middle of a sentence supports nothing, because the
			// sentence continues past it.
			name: "a fragment cut short of the sentence end",
			cand: Candidate{
				Key: "contact", Value: "wants to be pinged",
				Quote: "Ping me", Source: "stated",
			},
			wantReason: ReasonQuoteTooShort,
		},
		{
			name: "a fragment that ends a sentence but does not start one",
			cand: Candidate{
				Key: "prefers", Value: "works during the day",
				Quote: "the day.", Source: "stated",
			},
			wantReason: ReasonQuoteTooShort,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accepted, refused := Verify(p, turns, []Candidate{tc.cand})
			if tc.wantReason == "" {
				if len(accepted) != 1 {
					t.Fatalf("a stated fact was refused: %v", refused)
				}
				return
			}
			if len(accepted) != 0 {
				t.Fatalf("expected %q, but the fact was WRITTEN as %q: %q",
					tc.wantReason, accepted[0].Key, accepted[0].Value)
			}
			if len(refused) != 1 || refused[0].Reason != tc.wantReason {
				t.Fatalf("want %s, got %v", tc.wantReason, refused)
			}
		})
	}
}

// The exemption cannot become the hole the floor was plugging. A complete
// sentence that states nothing on its own — a bare assent — takes its
// meaning from the AGENT's question, which is precisely the laundering
// route memU's assistant-turn exclusion closes. Refused, with a reason
// that says which of the two things went wrong.
func TestVerify_ShortCompleteSentenceMustStateSomething(t *testing.T) {
	turns := []Turn{
		{Role: "assistant", Content: "So you want the digest daily, on Postgres, in Czech?"},
		{Role: "user", BySubject: true, Content: "Yes."},
		{Role: "user", BySubject: true, Content: "Ok, do it."},
		{Role: "user", BySubject: true, Content: "Czech."},
	}
	for _, tc := range []struct {
		name       string
		cand       Candidate
		wantReason string
	}{
		{
			name: "a bare yes, whose content is the agent's question",
			cand: Candidate{
				Key: "tooling", Value: "Postgres",
				Quote: "Yes.", Source: "stated",
			},
			wantReason: ReasonQuoteStatesNothing,
		},
		{
			name: "assent plus an instruction to act, still stating nothing",
			cand: Candidate{
				Key: "prefers", Value: "a daily digest",
				Quote: "Ok, do it.", Source: "stated",
			},
			wantReason: ReasonQuoteStatesNothing,
		},
		{
			name: "the same shape of answer, but it names the thing",
			cand: Candidate{
				Key: "language", Value: "Czech",
				Quote: "Czech.", Source: "stated",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc, ref := Verify(ProfileStatedTechnical, turns, []Candidate{tc.cand})
			if tc.wantReason == "" {
				if len(acc) != 1 {
					t.Fatalf("expected the fact to be written, refused: %v", ref)
				}
				return
			}
			if len(acc) != 0 {
				t.Fatalf("expected %q, wrote %v", tc.wantReason, acc)
			}
			if len(ref) != 1 || ref[0].Reason != tc.wantReason {
				t.Fatalf("want %s, got %v", tc.wantReason, ref)
			}
		})
	}
}

// The exemption is scoped to sentences the SUBJECT wrote. An agent's short
// sentence is not readmitted through it — that would hand the assistant's
// own words the one route the origin check exists to close.
func TestVerify_ShortSentenceExemptionIsSubjectOnly(t *testing.T) {
	turns := []Turn{
		{Role: "assistant", Content: "Postgres. That is what the rest of the team uses."},
		{Role: "user", BySubject: true, Content: "Let's talk about the deploy pipeline instead."},
		{Role: "user", BySubject: false, Content: "Neovim."},
	}
	for _, tc := range []struct {
		name  string
		quote string
	}{
		{"an agent's short sentence", "Postgres."},
		{"a third party's short sentence", "Neovim."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acc, ref := Verify(ProfileStatedTechnical, turns, []Candidate{{
				Key: "tooling", Value: "Postgres", Quote: tc.quote, Source: "stated",
			}})
			if len(acc) != 0 {
				t.Fatalf("a span the subject never wrote was admitted: %v", acc)
			}
			if len(ref) != 1 || ref[0].Reason != ReasonQuoteTooShort {
				t.Fatalf("want %s, got %v", ReasonQuoteTooShort, ref)
			}
		})
	}
}

// The exemption is a profile switch, not a compiled-in relaxation: a
// profile that turns it off gets the absolute character floor back.
func TestVerify_ShortSentenceExemptionIsAProfileSwitch(t *testing.T) {
	strict := ProfileStatedTechnical
	strict.Name = "stated-technical-strict"
	strict.AllowShortCompleteSentence = false

	cand := Candidate{Key: "timezone", Value: "UTC+1", Quote: "UTC+1.", Source: "stated"}
	if acc, _ := Verify(ProfileStatedTechnical, measuredTranscript(), []Candidate{cand}); len(acc) != 1 {
		t.Fatalf("the shipped profile must admit the answer")
	}
	acc, ref := Verify(strict, measuredTranscript(), []Candidate{cand})
	if len(acc) != 0 {
		t.Fatalf("the switch was ignored: %v", acc)
	}
	if len(ref) != 1 || ref[0].Reason != ReasonQuoteTooShort {
		t.Fatalf("want %s, got %v", ReasonQuoteTooShort, ref)
	}
}

// The prompt is rendered FROM the profile so it cannot drift from the
// gate. A profile that admits short complete sentences has to say so —
// a model that does not know it pads the span with the agent's words and
// gets refused for origin instead — and a profile that does not admit
// them must not advertise them.
func TestBuildSystemPrompt_TracksTheShortSentenceSwitch(t *testing.T) {
	on := BuildSystemPrompt(ProfileStatedTechnical)
	if !strings.Contains(on, `"UTC+1."`) {
		t.Errorf("the prompt does not tell the model a short whole sentence is a valid span")
	}
	strict := ProfileStatedTechnical
	strict.AllowShortCompleteSentence = false
	if strings.Contains(BuildSystemPrompt(strict), `"UTC+1."`) {
		t.Errorf("the prompt promises what a strict profile's gate refuses")
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
