package keepercfg

import (
	"context"
	"strings"
	"testing"
)

// --- built-in ---------------------------------------------------------------

// An instance nobody has configured must resolve to the built-in profile with
// every toggle attributed to it. The alternative — reporting SourceInstance for
// values nobody set — is what makes a config screen lie about who decided.
func TestProfile_UntouchedInstanceReportsBuiltIn(t *testing.T) {
	s := newTestStore(t, envDefaults)
	p := s.Effective().Profile

	if p.Name.Value != string(DefaultProfile) || p.Name.Source != SourceDefault {
		t.Errorf("name = %q/%s, want %q/default", p.Name.Value, p.Name.Source, DefaultProfile)
	}
	// The PRD P0 table: evidence and the hard gate on, precedent off, three
	// precedent examples if it were on, one sample (self-consistency off),
	// prompt budget carried by the preset rather than uncapped.
	if !p.Evidence.Value || p.Evidence.Source != SourceDefault {
		t.Errorf("evidence = %v/%s, want true/default", p.Evidence.Value, p.Evidence.Source)
	}
	if !p.HardGate.Value || p.HardGate.Source != SourceDefault {
		t.Errorf("hard_gate = %v/%s, want true/default", p.HardGate.Value, p.HardGate.Source)
	}
	if p.Precedent.Value || p.Precedent.Source != SourceDefault {
		t.Errorf("precedent = %v/%s, want false/default", p.Precedent.Value, p.Precedent.Source)
	}
	if p.PrecedentN.Value != 3 {
		t.Errorf("precedent_n = %d, want 3", p.PrecedentN.Value)
	}
	if p.ConsistencySamples.Value != 1 {
		t.Errorf("consistency_samples = %d, want 1", p.ConsistencySamples.Value)
	}
	// The default profile is lean, which targets the 4096-token reference judge.
	// A real number rather than 0: 0 means NO CAP, so an untouched instance would
	// otherwise ship with the P7 protection off while the docs said it was on.
	if p.PromptBudgetTokens.Value != 3500 {
		t.Errorf("prompt_budget_tokens = %d, want lean's 3500", p.PromptBudgetTokens.Value)
	}
	if len(p.EvidenceFacts.Value) != len(EvidenceFacts) {
		t.Errorf("evidence_facts = %v, want all %d facts", p.EvidenceFacts.Value, len(EvidenceFacts))
	}
	if p.Overridden {
		t.Error("Overridden = true with nothing stored")
	}
}

// --- presets ----------------------------------------------------------------

// A preset is a layer, not a write of seven values: selecting it must report
// SourceProfile so an operator can tell "the standard profile turned precedent
// on" apart from "somebody turned precedent on here".
func TestProfile_PresetDecidesAndSaysSo(t *testing.T) {
	cases := []struct {
		profile   ProfileName
		precedent bool
		samples   int64
	}{
		{ProfileLean, false, 1},
		{ProfileStandard, true, 1},
		{ProfileThorough, true, 3},
	}
	for _, tc := range cases {
		t.Run(string(tc.profile), func(t *testing.T) {
			s := newTestStore(t, envDefaults)
			name := string(tc.profile)
			eff, err := s.Apply(context.Background(), Patch{Profile: &name}, "u1")
			if err != nil {
				t.Fatalf("apply: %v", err)
			}
			p := eff.Profile
			if p.Name.Value != name || p.Name.Source != SourceInstance {
				t.Errorf("name = %q/%s, want %q/instance", p.Name.Value, p.Name.Source, name)
			}
			if p.Precedent.Value != tc.precedent || p.Precedent.Source != SourceProfile {
				t.Errorf("precedent = %v/%s, want %v/profile", p.Precedent.Value, p.Precedent.Source, tc.precedent)
			}
			if p.ConsistencySamples.Value != tc.samples || p.ConsistencySamples.Source != SourceProfile {
				t.Errorf("consistency_samples = %d/%s, want %d/profile",
					p.ConsistencySamples.Value, p.ConsistencySamples.Source, tc.samples)
			}
			if !p.Overridden {
				t.Error("Overridden = false after selecting a profile")
			}
		})
	}
}

// --- tri-state --------------------------------------------------------------

// The reason the toggles are tri-state at all: turning ONE capability off must
// not pin the other six to the values they happened to have that day. Here
// precedent is turned off by hand, then the profile is moved lean → thorough;
// the hand-set toggle survives and everything else follows the new profile.
func TestProfile_OverrideOneToggleLeavesTheRestFollowingTheProfile(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	lean := string(ProfileLean)
	off := TriOff
	if _, err := s.Apply(ctx, Patch{Profile: &lean, Precedent: &off}, "u1"); err != nil {
		t.Fatalf("apply lean: %v", err)
	}

	thorough := string(ProfileThorough)
	eff, err := s.Apply(ctx, Patch{Profile: &thorough}, "u1")
	if err != nil {
		t.Fatalf("apply thorough: %v", err)
	}
	p := eff.Profile
	if p.Precedent.Value || p.Precedent.Source != SourceInstance {
		t.Errorf("precedent = %v/%s, want false/instance — the explicit off must survive a profile change",
			p.Precedent.Value, p.Precedent.Source)
	}
	if p.ConsistencySamples.Value != 3 || p.ConsistencySamples.Source != SourceProfile {
		t.Errorf("consistency_samples = %d/%s, want 3/profile — an untouched toggle must follow the new profile",
			p.ConsistencySamples.Value, p.ConsistencySamples.Source)
	}
}

// `inherit` means "follow the profile", not "off". Clearing the override must
// hand the toggle back to the profile rather than freeze it at its last value.
func TestProfile_InheritReturnsTheToggleToTheProfile(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	standard := string(ProfileStandard)
	off := TriOff
	if _, err := s.Apply(ctx, Patch{Profile: &standard, Precedent: &off}, "u1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	inherit := TriInherit
	eff, err := s.Apply(ctx, Patch{Precedent: &inherit}, "u1")
	if err != nil {
		t.Fatalf("apply inherit: %v", err)
	}
	if !eff.Profile.Precedent.Value || eff.Profile.Precedent.Source != SourceProfile {
		t.Errorf("precedent = %v/%s, want true/profile after inherit",
			eff.Profile.Precedent.Value, eff.Profile.Precedent.Source)
	}
}

// Zero clears a numeric toggle back to the profile — the same convention the aux
// patch uses. It matters most for consistency_samples, where 1 is a MEANINGFUL
// value ("one sample, self-consistency off") and must not double as "unset".
func TestProfile_ZeroClearsNumericOverrideOneDisablesSampling(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	thorough := string(ProfileThorough)
	one := int64(1)
	eff, err := s.Apply(ctx, Patch{Profile: &thorough, ConsistencySamples: &one}, "u1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff.Profile.ConsistencySamples.Value != 1 || eff.Profile.ConsistencySamples.Source != SourceInstance {
		t.Fatalf("consistency_samples = %d/%s, want 1/instance",
			eff.Profile.ConsistencySamples.Value, eff.Profile.ConsistencySamples.Source)
	}
	zero := int64(0)
	eff, err = s.Apply(ctx, Patch{ConsistencySamples: &zero}, "u1")
	if err != nil {
		t.Fatalf("apply clear: %v", err)
	}
	if eff.Profile.ConsistencySamples.Value != 3 || eff.Profile.ConsistencySamples.Source != SourceProfile {
		t.Errorf("consistency_samples = %d/%s, want 3/profile after clearing",
			eff.Profile.ConsistencySamples.Value, eff.Profile.ConsistencySamples.Source)
	}
}

// --- evidence facts ---------------------------------------------------------

func TestProfile_EvidenceFactsSubset(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	// A preset is selected first so that clearing the list below has a profile to
	// fall back TO — the layer this test is about.
	standard := string(ProfileStandard)
	// Duplicated and space-padded on purpose: the CLI hands this through as the
	// operator typed it.
	facts := " credential_bound_to_agent , agent_denies_last_7d ,credential_bound_to_agent"
	eff, err := s.Apply(ctx, Patch{Profile: &standard, EvidenceFacts: &facts}, "u1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := eff.Profile.EvidenceFacts
	want := []string{"credential_bound_to_agent", "agent_denies_last_7d"}
	if len(got.Value) != len(want) {
		t.Fatalf("evidence_facts = %v, want %v", got.Value, want)
	}
	for i := range want {
		if got.Value[i] != want[i] {
			t.Fatalf("evidence_facts = %v, want %v (order preserved, duplicates dropped)", got.Value, want)
		}
	}
	if got.Source != SourceInstance {
		t.Errorf("evidence_facts source = %s, want instance", got.Source)
	}

	empty := ""
	eff, err = s.Apply(ctx, Patch{EvidenceFacts: &empty}, "u1")
	if err != nil {
		t.Fatalf("apply clear: %v", err)
	}
	if len(eff.Profile.EvidenceFacts.Value) != len(EvidenceFacts) || eff.Profile.EvidenceFacts.Source != SourceProfile {
		t.Errorf("evidence_facts = %v/%s, want all facts from the profile",
			eff.Profile.EvidenceFacts.Value, eff.Profile.EvidenceFacts.Source)
	}
}

// --- validation -------------------------------------------------------------

func TestProfile_Validation(t *testing.T) {
	cases := []struct {
		name  string
		patch Patch
		want  string
	}{
		{"unknown profile", Patch{Profile: strp("aggressive")}, "unknown judge profile"},
		{"unknown fact", Patch{EvidenceFacts: strp("crew_scope")}, "unknown evidence fact"},
		{"precedent_n too high", Patch{PrecedentN: i64p(99)}, "precedent example"},
		{"even sample count", Patch{ConsistencySamples: i64p(2)}, "odd"},
		{"budget too small", Patch{PromptBudgetTokens: i64p(64)}, "prompt budget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t, envDefaults)
			_, err := s.Apply(context.Background(), tc.patch, "u1")
			if err == nil {
				t.Fatalf("apply accepted %+v", tc.patch)
			}
			if !IsValidation(err) {
				t.Fatalf("error is not a validation error: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err.Error(), tc.want)
			}
		})
	}
}

// --- audit ------------------------------------------------------------------

// Two decisions taken under different judge capabilities are not comparable, so
// the audit needs the whole resolved profile, not just its name — a `standard`
// with precedent hand-disabled is a different judge from `standard`.
func TestProfile_StampDistinguishesWhatDecided(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	base := s.Effective().Profile.Stamp()
	if !strings.HasPrefix(base, string(DefaultProfile)) {
		t.Errorf("stamp %q does not start with the profile name", base)
	}

	standard := string(ProfileStandard)
	eff, err := s.Apply(ctx, Patch{Profile: &standard}, "u1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	withStandard := eff.Profile.Stamp()
	if withStandard == base {
		t.Fatalf("stamp unchanged (%q) after switching profile", withStandard)
	}

	off := TriOff
	eff, err = s.Apply(ctx, Patch{Precedent: &off}, "u1")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if eff.Profile.Stamp() == withStandard {
		t.Errorf("stamp unchanged (%q) after disabling precedent under the same profile", withStandard)
	}
}

// --- reset ------------------------------------------------------------------

func TestProfile_ResetReturnsToTheBuiltIn(t *testing.T) {
	s := newTestStore(t, envDefaults)
	ctx := context.Background()

	thorough := string(ProfileThorough)
	off := TriOff
	if _, err := s.Apply(ctx, Patch{Profile: &thorough, Evidence: &off}, "u1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff, err := s.Reset(ctx, "u1")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if eff.Profile.Name.Value != string(DefaultProfile) || eff.Profile.Name.Source != SourceDefault {
		t.Errorf("name = %q/%s, want %q/default", eff.Profile.Name.Value, eff.Profile.Name.Source, DefaultProfile)
	}
	if !eff.Profile.Evidence.Value || eff.Profile.Evidence.Source != SourceDefault {
		t.Errorf("evidence = %v/%s, want true/default", eff.Profile.Evidence.Value, eff.Profile.Evidence.Source)
	}
	if eff.Overridden {
		t.Error("Overridden = true after reset")
	}
}

// A profile edit must be visible to the NEXT decision without a restart. Two
// halves to that: the cache serves the new value immediately, and the
// fingerprint moves so the lazy gatekeeper — which is BUILT from an Effective
// and then cached — rebuilds instead of judging on the profile it booted with.
func TestProfile_ChangeIsVisibleWithoutReload(t *testing.T) {
	s := newTestStore(t, envDefaults)
	before := s.Effective().JudgeFingerprint()

	thorough := string(ProfileThorough)
	if _, err := s.Apply(context.Background(), Patch{Profile: &thorough}, "u1"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	eff := s.Effective()
	if got := eff.Profile.ConsistencySamples.Value; got != 3 {
		t.Errorf("consistency_samples = %d, want 3 straight from the cache", got)
	}
	if eff.JudgeFingerprint() == before {
		t.Error("the fingerprint did not move: a cached judge would keep the old profile")
	}
}

func i64p(v int64) *int64 { return &v }
