package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The "Test value" button in the credential form was gated on a FRONTEND flag,
// `BrandEntry.cli` in lib/credential-providers/registry.ts, which marks the five
// brands Crewship drives inside agent containers (Anthropic, OpenAI, Google,
// Cursor, Factory). The comment above that gate states the intent correctly —
// show the button only where a real upstream probe exists, otherwise it is "a
// placebo that returns no validation available".
//
// The intent is right; the predicate is wrong. probeProvider has real probes for
// GITHUB, GITLAB and VERCEL too, and none of those three is cli:true — so the
// button was hidden for three providers the server can actually validate. That
// is the whole of the user-visible symptom ("connectivity doesn't really work").
//
// The repair is not to flip the flag but to remove the second list: probe
// support is decided in Go, next to the probes themselves, and the frontend is
// told. Same shape as #1083, which single-sourced the provider→env-var map for
// exactly this drift reason.
//
// These tests are the lock. probeSupportedProviders is the one place a new probe
// gets registered; forget it and the golden set below fails rather than the
// button silently staying hidden.

// TestProbeSupported_GoldenSet pins the exact provider set with a real upstream
// probe. Adding a `case` to probeProvider without adding it here (or the
// reverse) fails.
func TestProbeSupported_GoldenSet(t *testing.T) {
	t.Parallel()

	supported := []string{
		"ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY",
		// The three the cli-flag gate was hiding.
		"GITHUB", "GITLAB", "VERCEL",
	}
	for _, p := range supported {
		if !probeSupported(p, "API_KEY") {
			t.Errorf("provider %s has an upstream probe in probeProvider but probeSupported says no; "+
				"the Test button will stay hidden for it", p)
		}
	}

	// Passive secrets: stored, handed to the agent, never probed by us. The
	// button must stay hidden — a "Test" that always says valid is worse than
	// no button.
	for _, p := range []string{"NOTION", "STRIPE", "LINEAR", "AWS", "KUBERNETES", "", "UNKNOWN_BRAND"} {
		if probeSupported(p, "API_KEY") {
			t.Errorf("provider %q has no upstream probe but probeSupported says yes; "+
				"the Test button would render a placebo", p)
		}
	}
}

// TestProbeSupported_UnsupportedProviderIsNotReportedValid guards the placebo
// directly at the source. probeProvider's default branch answers "No validation
// available for this provider" — an honest message wrapped in Valid:true, which
// renders as a green tick. Whatever the JSON `valid` field keeps saying for
// backward compatibility, `supported` must say false so no caller (UI or CLI)
// can mistake a non-check for a pass.
//
// Hermetic: unsupported providers take the default branch, which performs no
// network I/O.
func TestProbeSupported_UnsupportedProviderIsNotReportedValid(t *testing.T) {
	t.Parallel()

	res := probeProvider(t.Context(), "NOTION", "API_KEY", "secret_abc123", false)
	if res.Supported {
		t.Errorf("NOTION has no probe; result must not claim supported. got %+v", res)
	}
}

// TestProbeSupported_EndpointURLIsTestable keeps the one type-driven probe in
// the set. An ENDPOINT_URL is testable regardless of provider — the stored value
// IS the target — so support is a function of (provider, type), not provider
// alone.
func TestProbeSupported_EndpointURLIsTestable(t *testing.T) {
	t.Parallel()

	if !probeSupported("OLLAMA", string(CredTypeEndpointURL)) {
		t.Error("ENDPOINT_URL is probeable (probeLocalModelEndpoint) regardless of provider")
	}
	if probeSupported("OLLAMA", "API_KEY") {
		t.Error("OLLAMA with a non-endpoint type has no probe")
	}
}

// TestDefaultEnvVar_ExposesTestable is the wire that replaces the frontend's
// second list. The Add Credential wizard already calls this endpoint to learn
// the conventional env var for a provider; it now learns from the same call
// whether a Test button is worth rendering.
func TestDefaultEnvVar_ExposesTestable(t *testing.T) {
	t.Parallel()
	h, _ := newCredHandler(t)

	cases := map[string]struct {
		envVar   string
		testable bool
	}{
		"GITHUB": {"GH_TOKEN", true},     // was hidden by the cli gate
		"GITLAB": {"GITLAB_TOKEN", true}, // was hidden by the cli gate
		"VERCEL": {"VERCEL_TOKEN", true}, // was hidden by the cli gate
		// The two fields are independent, and these rows are the proof: a
		// provider can have a conventional env var and still have nothing to
		// probe against. Knowing where a secret goes says nothing about being
		// able to ask whether it works.
		"AWS":     {"AWS_ACCESS_KEY_ID", false},
		"NOTION":  {"NOTION_API_KEY", false},
		"UNKNOWN": {"", false},
	}

	for prov, want := range cases {
		req := httptest.NewRequest("GET", "/api/v1/credentials/default-env-var?provider="+prov, nil)
		rr := httptest.NewRecorder()
		h.DefaultEnvVar(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("provider=%s status=%d", prov, rr.Code)
			continue
		}
		var resp struct {
			EnvVar   string `json:"env_var"`
			Testable bool   `json:"testable"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Errorf("provider=%s unmarshal: %v", prov, err)
			continue
		}
		if resp.EnvVar != want.envVar {
			t.Errorf("provider=%s env_var=%q, want %q", prov, resp.EnvVar, want.envVar)
		}
		if resp.Testable != want.testable {
			t.Errorf("provider=%s testable=%v, want %v", prov, resp.Testable, want.testable)
		}
	}
}

// TestCredentialRead_TestableOnBothScanPaths keeps List and Get from parting
// ways on the field.
//
// credentialResponse is populated at two independent sites — scanCredentialRows
// for List, and an inline Scan in Get — each deriving computed fields (today
// EndpointURL, now Testable) with its own copy of the same three lines. That
// duplication is pre-existing and fine as long as something notices when only
// one side gets a new field: a detail sheet whose Test button depends on
// Testable would otherwise work from the list and vanish on open, or the
// reverse, with nothing failing.
func TestCredentialRead_TestableOnBothScanPaths(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// seedCredentialEnc writes provider GITHUB — probeable, so both paths must
	// say so. A provider with no probe is covered by TestProbeSupported_GoldenSet;
	// what is at stake here is the two paths agreeing, not the value itself.
	const credID = "cred-testable-both"
	seedCredentialEnc(t, db, wsID, userID, credID, "GH_TOKEN", "ghp_secret")

	listReq := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	listReq = listReq.WithContext(withWorkspace(listReq.Context(), wsID, "OWNER"))
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("List status=%d body=%s", listRR.Code, listRR.Body.String())
	}
	// Unpaginated List returns a bare array; the {credentials, next_cursor}
	// envelope appears only once ?limit is supplied.
	var listBody []struct {
		ID       string `json:"id"`
		Testable bool   `json:"testable"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("List unmarshal: %v", err)
	}
	var fromList *bool
	for i := range listBody {
		if listBody[i].ID == credID {
			fromList = &listBody[i].Testable
		}
	}
	if fromList == nil {
		t.Fatalf("seeded credential missing from List: %s", listRR.Body.String())
	}
	if !*fromList {
		t.Error("List reports a GITHUB credential as not testable; probeSupported says otherwise")
	}

	getReq := httptest.NewRequest("GET", "/api/v1/credentials/"+credID, nil)
	getReq.SetPathValue("credentialId", credID)
	getReq = getReq.WithContext(withWorkspace(getReq.Context(), wsID, "OWNER"))
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("Get status=%d body=%s", getRR.Code, getRR.Body.String())
	}
	var getBody struct {
		Testable bool `json:"testable"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &getBody); err != nil {
		t.Fatalf("Get unmarshal: %v", err)
	}
	if getBody.Testable != *fromList {
		t.Errorf("List and Get disagree on testable (list=%v get=%v) — one scan path "+
			"was updated and the other was not", *fromList, getBody.Testable)
	}
}
