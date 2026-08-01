package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// profileMock serves the judge-profile half of /admin/keeper/config and records
// what the command sent. Separate from keeperMock because these tests are about
// the PUT body shape, and the shared mock echoes a fixed config that says
// nothing about the profile.
type profileMock struct {
	mu   sync.Mutex
	body []byte
}

func (m *profileMock) lastBody(t *testing.T) map[string]any {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.body) == 0 {
		t.Fatal("no PUT /admin/keeper/config was issued")
	}
	var out map[string]any
	if err := json.Unmarshal(m.body, &out); err != nil {
		t.Fatalf("decode PUT body %q: %v", m.body, err)
	}
	return out
}

func startProfileMock(t *testing.T) *profileMock {
	t.Helper()
	saveCLIState(t)

	m := &profileMock{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/keeper/config" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			m.body = b
			m.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"judge_profile": map[string]any{
				"name":                 map[string]any{"value": "standard", "source": "instance"},
				"evidence":             map[string]any{"value": true, "source": "profile"},
				"evidence_facts":       map[string]any{"value": []string{"credential_bound_to_agent"}, "source": "instance"},
				"hard_gate":            map[string]any{"value": true, "source": "profile"},
				"precedent":            map[string]any{"value": false, "source": "instance"},
				"precedent_n":          map[string]any{"value": 3, "source": "profile"},
				"consistency_samples":  map[string]any{"value": 1, "source": "profile"},
				"prompt_budget_tokens": map[string]any{"value": 0, "source": "profile"},
				"overridden":           true,
				"choices":              []string{"lean", "standard", "thorough"},
				"available_facts":      []string{"credential_bound_to_agent", "agent_denies_last_7d"},
				"stamp":                "standard evidence=on facts=credential_bound_to_agent hard_gate=on precedent=off/3 samples=1 budget=auto",
			},
		})
	}))
	t.Cleanup(srv.Close)

	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: "cabcdefghijklmnopqrs", Server: srv.URL}
	return m
}

// resetKeeperProfileFlags puts the package-level cobra flags back to zero — a
// test that set --precedent would otherwise leak into the next one.
func resetKeeperProfileFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		flagKeeperProfileEvidence, flagKeeperProfileFacts = "", ""
		flagKeeperProfileHardGate, flagKeeperProfilePrecedent = "", ""
		flagKeeperProfilePrecedentN, flagKeeperProfileSamples = "", ""
		flagKeeperProfilePromptBudget = ""
		for _, name := range []string{"evidence", "evidence-facts", "hard-gate", "precedent",
			"precedent-n", "consistency-samples", "prompt-budget"} {
			if f := keeperProfileSetCmd.Flags().Lookup(name); f != nil {
				f.Changed = false
			}
		}
	})
}

func TestKeeperProfileCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range keeperCmd.Commands() {
		have[sub.Name()] = true
	}
	if !have["profile"] {
		t.Fatalf("keeper missing subcommand %q; have %v", "profile", have)
	}
	haveSub := map[string]bool{}
	for _, sub := range keeperProfileCmd.Commands() {
		haveSub[sub.Name()] = true
	}
	for _, want := range []string{"get", "set", "reset"} {
		if !haveSub[want] {
			t.Errorf("keeper profile missing subcommand %q; have %v", want, haveSub)
		}
	}
}

func TestKeeperProfileGetRunE(t *testing.T) {
	startProfileMock(t)
	if err := keeperProfileGetCmd.RunE(keeperProfileGetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

// The distinction the whole feature rests on: 'inherit' must reach the server as
// JSON null (follow the profile), never as false. If it were sent as false, an
// operator "undoing" a change would instead pin the capability off forever.
func TestKeeperProfileSet_InheritIsNullNotFalse(t *testing.T) {
	m := startProfileMock(t)
	resetKeeperProfileFlags(t)

	if err := keeperProfileSetCmd.Flags().Set("precedent", "inherit"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperProfileSetCmd.Flags().Set("evidence", "off"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperProfileSetCmd.RunE(keeperProfileSetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	body := m.lastBody(t)
	v, present := body["judge_precedent"]
	if !present {
		t.Fatalf("judge_precedent absent from %v", body)
	}
	if v != nil {
		t.Errorf("judge_precedent = %v, want null (inherit)", v)
	}
	if body["judge_evidence"] != false {
		t.Errorf("judge_evidence = %v, want false", body["judge_evidence"])
	}
	// Only what was passed: an untouched toggle must not be written at today's
	// value, or two operators editing different toggles clobber each other.
	for _, unwanted := range []string{"judge_hard_gate", "judge_profile", "judge_consistency_samples"} {
		if _, sent := body[unwanted]; sent {
			t.Errorf("%s was sent without being passed: %v", unwanted, body)
		}
	}
}

func TestKeeperProfileSet_PresetAndOverrideInOneCall(t *testing.T) {
	m := startProfileMock(t)
	resetKeeperProfileFlags(t)

	if err := keeperProfileSetCmd.Flags().Set("consistency-samples", "3"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperProfileSetCmd.RunE(keeperProfileSetCmd, []string{"thorough"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.lastBody(t)
	if body["judge_profile"] != "thorough" {
		t.Errorf("judge_profile = %v, want thorough", body["judge_profile"])
	}
	if body["judge_consistency_samples"] != float64(3) {
		t.Errorf("judge_consistency_samples = %v, want 3", body["judge_consistency_samples"])
	}
}

// An empty numeric flag is the documented way to go back to the profile, and it
// must be distinguishable from not passing the flag at all.
func TestKeeperProfileSet_EmptyNumberClearsTheOverride(t *testing.T) {
	m := startProfileMock(t)
	resetKeeperProfileFlags(t)

	if err := keeperProfileSetCmd.Flags().Set("consistency-samples", ""); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperProfileSetCmd.RunE(keeperProfileSetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := m.lastBody(t)["judge_consistency_samples"]; got != float64(0) {
		t.Errorf("judge_consistency_samples = %v, want 0 (clear)", got)
	}
}

func TestKeeperProfileSet_RejectsUnknownProfileAndBadToggle(t *testing.T) {
	startProfileMock(t)
	resetKeeperProfileFlags(t)

	if err := keeperProfileSetCmd.RunE(keeperProfileSetCmd, []string{"aggressive"}); err == nil {
		t.Error("an unknown profile name was accepted")
	}
	if err := keeperProfileSetCmd.Flags().Set("precedent", "sometimes"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if err := keeperProfileSetCmd.RunE(keeperProfileSetCmd, nil); err == nil {
		t.Error("an unparsable toggle value was accepted")
	}
}

func TestKeeperProfileSet_NothingToChange(t *testing.T) {
	startProfileMock(t)
	resetKeeperProfileFlags(t)

	if err := keeperProfileSetCmd.RunE(keeperProfileSetCmd, nil); err == nil {
		t.Error("an empty set was accepted; it would PUT an empty body")
	}
}

// Reset must clear the profile fields ONLY. Sending DELETE would drop the judge
// endpoint and model with them, i.e. un-configure the judge an operator was only
// re-tuning.
func TestKeeperProfileReset_ClearsProfileFieldsWithoutTouchingTheWiring(t *testing.T) {
	m := startProfileMock(t)

	if err := keeperProfileResetCmd.RunE(keeperProfileResetCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.lastBody(t)
	for _, field := range []string{"judge_evidence", "judge_hard_gate", "judge_precedent"} {
		v, present := body[field]
		if !present || v != nil {
			t.Errorf("%s = %v (present=%v), want null", field, v, present)
		}
	}
	if body["judge_profile"] != "" {
		t.Errorf("judge_profile = %v, want \"\"", body["judge_profile"])
	}
	for _, field := range []string{"judge_endpoint_url", "judge_model", "enabled", "judge_timeout_ms"} {
		if _, sent := body[field]; sent {
			t.Errorf("%s was sent by a profile reset: %v", field, body)
		}
	}
}
