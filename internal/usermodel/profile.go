// Package usermodel extracts the small, durable profile an agent reads
// about the human it works for — the [OPERATOR MODEL] block — from what
// that person actually said.
//
// It exists because both extraction sweeps shipped wired to a no-op
// (consolidate.NoopUserModelExtractor), so the schema, the sweep, the
// merge, the opt-out and the two prompt blocks were all live while the
// files they read were always empty (#1669).
//
// # The rule this package implements
//
// Stated only. A fact is written when the person said it, in their own
// turn, in words this package can point at. Never what the system
// concluded from their behaviour. An inference is often wrong, the
// person cannot rebut it because they do not know it was made, and once
// written it reads as fact in the next session — the prompt carries no
// notion of provenance to mark it otherwise.
//
// And technical, not emotionally coloured: role, ownership, declared
// working preferences, standing constraints. Not sentiment, mood, or
// personality. "User asked that commits carry no co-author trailer" is
// admissible; "User seems frustrated with the review tooling" is not.
//
// # Why the prompt is not the guarantee
//
// Because measurement says it cannot be. "Manufactured Confidence"
// (arXiv:2606.29279) evaluated Zep/Graphiti WITH their attribution
// prompts in place and still measured attribution laundering in 12 of 12
// trials and hearsay in 11 of 12. Every serious project moved the
// guarantee out of the prompt, and so does this one:
//
//   - Verify (verify.go) admits a fact only when its evidence quote is
//     found, whitespace-collapsed but otherwise byte-identical, inside a
//     turn the SUBJECT authored. The model chooses which span; it cannot
//     author one. That mechanism is OpenClaw's, and it is the strongest
//     one in the field survey precisely because it is not a prompt.
//   - The key set is closed. A fact whose key is not in the profile is
//     refused before its value is ever read, which is the checkable form
//     of "no sentiment": there is no key for a mood.
//   - The profile decides which SourceTypes are admissible at all. In
//     the shipped profile that set is {stated}, so there is no code path
//     an inference can travel — the mode is switched structurally, by
//     what is bound, not by how firmly the prompt asks.
//
// The prompt (prompt.go) still says all of this, because a model that is
// told the rule produces fewer refusals and refusals are wasted tokens.
// It is the suspenders; Verify is the belt.
package usermodel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// SourceType is where a candidate fact came from. The vocabulary is
// Personis's (Kay & Kummerfeld, Univ. Sydney, 2002), by way of
// Veracium's SourceType — an evidence-typed user model is a 24-year-old
// idea and the reason "stated only" is a WRITE POLICY rather than an
// architecture. Relaxing it later means admitting another SourceType
// here; it needs no migration, because nothing about the storage layer
// assumes the current answer.
type SourceType string

const (
	// SourceStated — the person said it, in their own turn. The only
	// admissible source in the shipped profile.
	SourceStated SourceType = "stated"

	// SourceObserved — visible in what the person did, but never said.
	// Defined so the vocabulary is complete and so a candidate that
	// declares itself observed is refused with an accurate reason rather
	// than an unrecognised-enum one. No shipped profile admits it.
	SourceObserved SourceType = "observed"

	// SourceInferred — concluded by the model. No shipped profile admits
	// it; see the package doc for why.
	SourceInferred SourceType = "inferred"
)

// Key is one admissible field of the operator model. Name is the bullet
// key as it lands on disk ("- role: …"); Desc is rendered into the
// extraction prompt so the model learns the field from an example rather
// than from a category name.
//
// Enumerating exemplars rather than naming a category is deliberate and
// borrowed from Graphiti, whose anti-emotion rule works because it lists
// them ("joy, balance, growth, resilience, happiness, passion,
// motivation") instead of saying "no feelings". That rule is in
// extract_message / extract_text, not in the extract_attributes prompt
// the value-origin whitelist comes from — see prompt.go.
type Key struct {
	Name string
	Desc string
}

// Profile is the extraction policy: which sources may be written, which
// fields exist, and how long a value may be.
//
// It is resolved per sweep from instance configuration (SettingProfile),
// not compiled in, so the judgement encoded here is the operator's to
// change without a rebuild. Priority is the declaration order of Keys:
// when a merged model exceeds the on-disk cap, the LAST keys are the
// ones dropped, so identity and ownership survive a squeeze that
// tooling trivia does not.
type Profile struct {
	// Name is the value of the SettingProfile instance setting that
	// selects this profile.
	Name string

	// Admissible is the closed set of SourceTypes that may be written.
	// Empty means the profile writes nothing at all.
	Admissible []SourceType

	// Keys is the closed, ordered set of admissible fields.
	Keys []Key

	// MaxValueChars bounds one field's value. A value over it is refused
	// rather than truncated: half a stated fact is not a stated fact.
	MaxValueChars int

	// MinQuoteChars is the shortest evidence span that can support a
	// fact. Without a floor the model can quote "I" and hang any claim
	// off it, which makes the byte-equality check ornamental.
	MinQuoteChars int

	// AllowShortCompleteSentence admits a span BELOW MinQuoteChars when
	// it is a complete sentence the subject wrote and that sentence
	// states something on its own.
	//
	// The floor is right about a fragment and wrong about an answer.
	// Measured under #1698 against a real claude-haiku-4-5: of 57
	// candidates, the only four refusals were this floor firing on
	// "UTC+1." (6 chars) and "Weekdays." (9), both true facts the person
	// had stated in reply to a direct question — while "reach via Slack,
	// not email", from the same sentence, stored every time. The
	// information density of an answer is inversely related to its
	// length, so a character floor systematically loses the fields that
	// are answered in one word: timezone, language, short-form prefers.
	//
	// Lowering the number instead would readmit the "I"-class span the
	// floor exists to refuse. The distinguishing property is not length
	// but completeness: a sentence the person finished is not a fragment
	// cherry-picked to support a claim. Bare assent is excluded on top of
	// that, because "Yes." is complete and still takes its content from
	// the AGENT's question — the laundering route the assistant-turn
	// exclusion closes.
	//
	// False keeps MinQuoteChars absolute. Every other check — subject
	// origin, byte equality, the closed key set, imperative phrasing,
	// trend language — applies unchanged either way; this relaxes the
	// length gate and nothing else.
	AllowShortCompleteSentence bool
}

// SettingProfile is the app_settings key selecting the extraction
// profile, read fresh on every sweep. It sits with the other instance
// settings (runtime.*, see internal/api/runtime_capacity_policy.go) in
// the same app_settings table and is settable with
// `crewship instance settings set memory.user_model_profile <name>`.
//
// Read per sweep rather than captured at boot on purpose: #1606 and
// #1556 are both the same bug — a runtime-settable knob resolved once at
// construction is a knob that needs a server restart, and neither the
// operator nor the docs expect one.
const SettingProfile = "memory.user_model_profile"

// ProfileStatedTechnical is the shipped default.
//
// Its key set answers "what does this person do, own, and want" and has
// no field a mood could be recorded in. Ordered by what should survive
// the cap: who they are, then what they own, then how they want to be
// worked with, then the incidentals.
var ProfileStatedTechnical = Profile{
	Name:       "stated-technical",
	Admissible: []SourceType{SourceStated},
	Keys: []Key{
		{"role", `what the person said their job or function is — "I run platform engineering", "I'm the release manager"`},
		{"owns", `systems, repositories or areas the person said they are responsible for — "the billing service is mine", "I own the deploy pipeline"`},
		{"constraint", `a standing limit the person stated — "commits must not carry a co-author trailer", "we cannot use hosted CI"`},
		{"process", `a working practice the person said they follow or require — "we review every migration before merge"`},
		{"prefers", `a working preference the person stated in words — "I prefer short answers", "give me the diff, not a summary"`},
		{"tooling", `tools, languages or platforms the person said they use — "we're on Postgres", "I work in Neovim"`},
		{"timezone", `the person's stated timezone or working hours — "I'm UTC+1", "I'm offline after 17:00"`},
		{"language", `the language the person said they want to be addressed in — "answer me in Czech"`},
		{"contact", `how the person said they want to be reached about work — "ping me on Slack, not email"`},
	},
	MaxValueChars:              160,
	MinQuoteChars:              12,
	AllowShortCompleteSentence: true,
}

// ProfileOff writes nothing. It is a real profile rather than a nil
// extractor so that "the operator turned this off" and "the wiring is
// broken" stay distinguishable in the sweep summary, and so the setting
// has a documented value that disables the feature without disabling
// the opt-out purge the same sweep performs.
var ProfileOff = Profile{
	Name:          "off",
	Admissible:    nil,
	Keys:          nil,
	MaxValueChars: 0,
	MinQuoteChars: 0,
}

// DefaultProfileName is used when the instance setting is absent or
// unreadable. On by default, matching every comparable system: Letta
// gives every agent a `human` block, Hermes ships USER.md, Zep Cloud
// includes a user summary in every context block.
const DefaultProfileName = "stated-technical"

// profiles is the registry the setting selects from. A profile that is
// not here cannot be selected, which is what makes the mode structural:
// there is no way to spell "also record what you inferred" that resolves
// to a Profile whose Admissible set contains it.
var profiles = map[string]Profile{
	ProfileStatedTechnical.Name: ProfileStatedTechnical,
	ProfileOff.Name:             ProfileOff,
}

// ResolveProfile maps a configured name to a profile. An empty name is
// the default; an unknown one is an error rather than a silent fallback,
// because "I set the profile and nothing changed" is exactly the failure
// this whole feature is meant not to reproduce.
func ResolveProfile(name string) (Profile, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = DefaultProfileName
	}
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("usermodel: unknown extraction profile %q (known: %s)",
			name, strings.Join(ProfileNames(), ", "))
	}
	return p, nil
}

// ProfileNames lists every selectable profile, sorted, for error text
// and for the docs to stay honest about what exists.
func ProfileNames() []string {
	out := make([]string, 0, len(profiles))
	for n := range profiles {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Writes reports whether this profile can admit anything at all.
func (p Profile) Writes() bool { return len(p.Admissible) > 0 && len(p.Keys) > 0 }

// admits reports whether a SourceType may be written under this profile.
func (p Profile) admits(s SourceType) bool {
	for _, a := range p.Admissible {
		if a == s {
			return true
		}
	}
	return false
}

// hasKey reports whether name is an admissible field, and where it sits
// in the priority order.
func (p Profile) hasKey(name string) (int, bool) {
	for i, k := range p.Keys {
		if k.Name == name {
			return i, true
		}
	}
	return 0, false
}

// ProfileFromSettings returns a ProfileReader backed by the app_settings
// table — the same instance-settings store the runtime.* keys use, and
// the same one `crewship instance settings set` writes.
//
// Read on every call rather than captured, so an operator changing the
// profile is in force on the NEXT SWEEP rather than the next server
// restart. That is not a nicety: #1606 and #1556 were both this bug in
// other subsystems, and a daily sweep inside a process that runs for
// weeks is the worst place to repeat it.
//
// An unreadable table yields the default — the feature stays on rather
// than silently switching off on a transient DB error. An unrecognised
// value yields "off" with a warning, because a profile name nobody
// implements must never resolve to one that writes.
func ProfileFromSettings(db *sql.DB, logger *slog.Logger) ProfileReader {
	return func(ctx context.Context) Profile {
		name := DefaultProfileName
		if db != nil {
			var v string
			err := db.QueryRowContext(ctx,
				`SELECT value FROM app_settings WHERE key = ?`, SettingProfile).Scan(&v)
			switch {
			case err == nil && strings.TrimSpace(v) != "":
				name = v
			case err != nil && !errors.Is(err, sql.ErrNoRows) && logger != nil:
				logger.Warn("user model: settings read failed; using the default profile",
					"key", SettingProfile, "profile", DefaultProfileName, "error", err)
			}
		}
		p, err := ResolveProfile(name)
		if err != nil {
			if logger != nil {
				logger.Warn("user model: unknown extraction profile; writing nothing",
					"key", SettingProfile, "configured", name, "error", err)
			}
			return ProfileOff
		}
		return p
	}
}

// KeyOrder returns the profile's key names in priority order — the order
// a merged model is rendered in and the reverse of the order fields are
// dropped in when the on-disk cap bites.
func (p Profile) KeyOrder() []string {
	out := make([]string, 0, len(p.Keys))
	for _, k := range p.Keys {
		out = append(out, k.Name)
	}
	return out
}
