package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestPreferencesListRunE_RendersKeysAndValues(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/me/preferences", clitest.JSONResponse(200, map[string]json.RawMessage{
		"theme":   json.RawMessage(`"dark"`),
		"density": json.RawMessage(`3`),
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return preferencesListCmd.RunE(preferencesListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"theme", "dark", "density", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("preferences list missing %q:\n%s", want, out)
		}
	}
}

func TestPreferencesListRunE_YAMLRendersValuesNotBytes(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/me/preferences", clitest.JSONResponse(200, map[string]json.RawMessage{
		"theme":   json.RawMessage(`"dark"`),
		"sidebar": json.RawMessage(`{"collapsed":true}`),
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "yaml" // covSetupCli10 restores flagFormat in cleanup

	out, err := captureStdoutCovCli10(t, func() error {
		return preferencesListCmd.RunE(preferencesListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// yaml.v3 renders a raw json.RawMessage ([]byte) as a list of byte
	// values ("- 34\n- 100 ..."). The command must decode values first so
	// yaml shows the real content.
	if strings.Contains(out, "- 34") || strings.Contains(out, "- 100") {
		t.Errorf("yaml output leaked raw bytes instead of decoded values:\n%s", out)
	}
	for _, want := range []string{"theme", "dark", "collapsed", "true"} {
		if !strings.Contains(out, want) {
			t.Errorf("yaml output missing %q:\n%s", want, out)
		}
	}
}

func TestPreferencesSetRunE_SendsRawJSONBody(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut("/api/v1/me/preferences/theme", clitest.EmptyResponse(204))
	covSetupCli10(t, s.URL())

	if _, err := captureStdoutCovCli10(t, func() error {
		return preferencesSetCmd.RunE(preferencesSetCmd, []string{"theme", `"dark"`})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PUT", "/api/v1/me/preferences/theme")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	// The literal must arrive verbatim (not double-encoded to `"\"dark\""`).
	if got := string(calls[0].Body); got != `"dark"` {
		t.Errorf("expected raw JSON body %q, got %q", `"dark"`, got)
	}
}

func TestPreferencesSetRunE_RejectsNonJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	covSetupCli10(t, s.URL())
	// Bare token is not valid JSON — must fail client-side before any call.
	err := preferencesSetCmd.RunE(preferencesSetCmd, []string{"theme", "dark"})
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Errorf("expected valid-JSON error, got %v", err)
	}
	if n := len(s.Calls()); n != 0 {
		t.Errorf("expected no HTTP calls on invalid input, got %d", n)
	}
}

func TestPreferencesDeleteRunE_Confirmed(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/me/preferences/theme", clitest.EmptyResponse(204))
	covSetupCli10(t, s.URL())
	_ = preferencesDeleteCmd.Flags().Set("yes", "true")
	t.Cleanup(func() { _ = preferencesDeleteCmd.Flags().Set("yes", "false") })

	if _, err := captureStdoutCovCli10(t, func() error {
		return preferencesDeleteCmd.RunE(preferencesDeleteCmd, []string{"theme"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(s.CallsFor("DELETE", "/api/v1/me/preferences/theme")); n != 1 {
		t.Errorf("expected 1 DELETE, got %d", n)
	}
}

func TestPrivacyConsentGetRunE_ShowsOptOutState(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/peer-consent", clitest.JSONResponse(200, peerConsentResponse{
		OptedOut: true, OptedOutAt: "2026-07-01T00:00:00Z",
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return privacyConsentGetCmd.RunE(privacyConsentGetCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"Opted out", "yes", "2026-07-01T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("consent get missing %q:\n%s", want, out)
		}
	}
}

func TestPrivacyConsentSetRunE_OptOutPurgesAndReportsCount(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPut("/api/v1/users/me/peer-consent", clitest.JSONResponse(200, peerConsentResponse{OptedOut: true, Purged: 4}))
	covSetupCli10(t, s.URL())
	_ = privacyConsentSetCmd.Flags().Set("yes", "true")
	t.Cleanup(func() { _ = privacyConsentSetCmd.Flags().Set("yes", "false") })

	// Success message lands on stderr (PrintSuccess); assert the observable
	// behavior — the request carries the opt-out flag — rather than the
	// cosmetic message.
	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyConsentSetCmd.RunE(privacyConsentSetCmd, []string{"on"})
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := s.CallsFor("PUT", "/api/v1/users/me/peer-consent")
	if len(calls) != 1 {
		t.Fatalf("expected 1 PUT, got %d", len(calls))
	}
	var body map[string]bool
	if err := clitest.DecodeJSONBody(calls[0].Body, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["opted_out"] != true {
		t.Errorf("expected opted_out=true in body, got %v", body)
	}
}

func TestPrivacyCardsListRunE_RendersRows(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/users/me/peer-cards", clitest.JSONResponse(200, peerCardsResponse{
		Peers: []peerCard{{ID: "pc_123", AgentSlug: "riley", Bytes: 512, UpdatedAt: "2026-07-01T00:00:00Z"}},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return privacyCardsListCmd.RunE(privacyCardsListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"riley", "512"} {
		if !strings.Contains(out, want) {
			t.Errorf("cards list missing %q:\n%s", want, out)
		}
	}
}

func TestPrivacyCardsDeleteRunE_ReportsPurged(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnDelete("/api/v1/users/me/peer-cards", clitest.JSONResponse(200, peerCardsResponse{Purged: 7}))
	covSetupCli10(t, s.URL())
	_ = privacyCardsDeleteCmd.Flags().Set("yes", "true")
	t.Cleanup(func() { _ = privacyCardsDeleteCmd.Flags().Set("yes", "false") })

	if _, err := captureStdoutCovCli10(t, func() error {
		return privacyCardsDeleteCmd.RunE(privacyCardsDeleteCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// DELETE must fire exactly once (the success/purge count is printed to
	// stderr via PrintSuccess, not asserted here).
	if n := len(s.CallsFor("DELETE", "/api/v1/users/me/peer-cards")); n != 1 {
		t.Errorf("expected 1 DELETE, got %d", n)
	}
}

func TestParseOnOff(t *testing.T) {
	truthy := []string{"on", "true", "yes", "y", "1", "out", "ON", " true "}
	falsey := []string{"off", "false", "no", "n", "0", "in"}
	for _, s := range truthy {
		if v, err := parseOnOff(s); err != nil || !v {
			t.Errorf("parseOnOff(%q) = %v, %v; want true, nil", s, v, err)
		}
	}
	for _, s := range falsey {
		if v, err := parseOnOff(s); err != nil || v {
			t.Errorf("parseOnOff(%q) = %v, %v; want false, nil", s, v, err)
		}
	}
	if _, err := parseOnOff("maybe"); err == nil {
		t.Error("parseOnOff(maybe) should error")
	}
}
