package api

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
// gets registered; forget it and the derived check below fails rather than the
// button silently staying hidden.

// TestProbeSupported_ProbelessRegistrationFails is the repaired golden test, and
// the repair is the point.
//
// Its predecessor (TestProbeSupported_GoldenSet) claimed in its own comment to
// fail in BOTH directions. It did not. The positive half walked a literal list
// of the eight providers that had probes; the negative half walked a second
// literal — {NOTION, STRIPE, LINEAR, AWS, KUBERNETES, "", UNKNOWN_BRAND} — so
// the only registrations it could catch were registrations of those seven names.
// Adding any OTHER provider to probeSupportedProviders with no matching arm in
// probeProviderInner passed green, and shipped exactly the placebo this file
// exists to prevent: a Valid:true / Supported:true green tick sitting on top of
// "No validation available for this provider".
//
// The negative half is now DERIVED. Every id actually in the map is driven
// through probeProviderInner and must land somewhere other than the default
// branch. Hermetic: the context is cancelled before the call, so each arm's
// http.DefaultClient.Do returns ctx.Err() before any DNS lookup or dial — a
// transport error, but still proof the arm exists, which is the only thing
// under test here.
func TestProbeSupported_ProbelessRegistrationFails(t *testing.T) {
	t.Parallel()

	// The exact set, pinned. Losing a probe (or gaining one nobody meant to
	// advertise) shows up here as a diff on this list.
	want := []string{
		"ANTHROPIC", "OPENAI", "GOOGLE",
		// OpenRouter: reachable by pasting a credential alone (phase 2), so
		// the wizard's Test button is the first thing that tells an operator
		// the key works.
		"OPENROUTER",
		// OPENAI_COMPAT (#2043): the operator supplies the host, so it is the
		// credential with the most ways to be wrong and was the only LLM
		// provider with no probe at all. Reachability only — the arm offers
		// neither the stored apiKey nor the custom headers.
		"OPENAI_COMPAT",
		"CURSOR", "FACTORY",
		// The three the frontend cli-flag gate was hiding.
		"GITHUB", "GITLAB", "VERCEL",
	}
	for _, p := range want {
		if !probeSupported(p, "API_KEY") {
			t.Errorf("provider %s has an upstream probe in probeProvider but probeSupported says no; "+
				"the Test button will stay hidden for it", p)
		}
	}
	if len(probeSupportedProviders) != len(want) {
		t.Errorf("probeSupportedProviders has %d entries, want %d (%v) — a provider was registered "+
			"without being pinned here", len(probeSupportedProviders), len(want), want)
	}

	// The derived half: registration without a probe must fail.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for p := range probeSupportedProviders {
		res := probeProviderInner(ctx, p, "API_KEY", "probe-fixture-not-a-real-key", false)
		if res.Error == probeNoValidationMsg {
			t.Errorf("provider %q is advertised as probeable but probeProviderInner falls through to "+
				"the default branch; the wizard would render a green tick over a non-check", p)
		}
	}
}

// TestProbeSupported_UnregisteredProviderStaysUnsupported is the other
// direction, and it is deliberately NOT a literal list of brand names — the
// point of the previous test's failure was that a literal list only ever knows
// about the names somebody thought to write down. A provider absent from the
// map must report unsupported whatever it is called.
func TestProbeSupported_UnregisteredProviderStaysUnsupported(t *testing.T) {
	t.Parallel()

	// OPENAI_COMPAT used to sit in this list and now carries a probe (#2043),
	// which is why the fixtures are checked against the map below rather than
	// trusted: a name that quietly gains a probe would otherwise turn this test
	// into one that asserts nothing.
	for _, p := range []string{"NOTION", "AWS", "BEDROCK", "", "UNKNOWN_BRAND", "openrouter", "openai_compat"} {
		if _, registered := probeSupportedProviders[p]; registered {
			t.Fatalf("fixture %q is registered; pick a name that is not, or this proves nothing", p)
		}
		if probeSupported(p, "API_KEY") {
			t.Errorf("provider %q has no upstream probe but probeSupported says yes; "+
				"the Test button would render a placebo", p)
		}
	}
}

// TestOpenRouterProbeResult pins the status→result mapping the OPENROUTER arm
// applies. It is the half of the probe that can be tested without a network,
// and the half that carries the judgement: 429 must stay Valid, because a
// throttled key is a working key and reporting it invalid sends the operator to
// rotate a credential that was fine.
func TestOpenRouterProbeResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantValid bool
		wantErr   string // substring; "" means no error text
	}{
		{"200 is a working key", 200, true, ""},
		{"401 is invalid", 401, false, "Invalid OpenRouter API key"},
		{"403 is disabled, not invalid", 403, false, "disabled or lacks access"},
		{"429 is valid but throttled", 429, true, "Rate limited"},
		{"500 is neither valid nor a verdict on the key", 500, false, "Unexpected response: 500"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := openRouterProbeResult(tt.status)
			if got.Valid != tt.wantValid {
				t.Errorf("openRouterProbeResult(%d).Valid = %v, want %v", tt.status, got.Valid, tt.wantValid)
			}
			if got.Status != tt.status {
				t.Errorf("openRouterProbeResult(%d).Status = %d, want %d", tt.status, got.Status, tt.status)
			}
			if tt.wantErr == "" {
				if got.Error != "" {
					t.Errorf("openRouterProbeResult(%d).Error = %q, want none", tt.status, got.Error)
				}
				return
			}
			if !strings.Contains(got.Error, tt.wantErr) {
				t.Errorf("openRouterProbeResult(%d).Error = %q, want substring %q", tt.status, got.Error, tt.wantErr)
			}
		})
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
		// The phase-2 pair, and the two fields' independence again — from the
		// other side this time. OpenRouter has both: a conventional variable an
		// unrouted OpenCode crew still reads, and a probe.
		//
		// OPENAI_COMPAT has a probe and no env var, and both are deliberate. Its
		// credential is an endpoint object no agent-side tool reads from env, so
		// there is no conventional variable to name. It became testable in #2043:
		// the wizard's pre-save Test syntax-checks the endpoint without dialling
		// (the body path is RequireAuth-only), and says so in its result text;
		// `credential test-stored` is what actually reaches the host, from the
		// role-gated path.
		"OPENROUTER":    {"OPENROUTER_API_KEY", true},
		"OPENAI_COMPAT": {"", true},
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

// TestCredentialRead_ExposesSensitivity closes the gap the UI work surfaced.
//
// sensitivity decides whether a credential can be revealed at all — SEALED is
// refused for every role including OWNER — but it was write-only: set through
// PUT /{id}/sensitivity, echoed by the reveal and sensitivity responses, and
// absent from every credential READ. So a client could gate its reveal
// affordance on the workspace switch, the capability and the role floor, and
// then had to let the user click and take a 403 for the fourth.
//
// That is the same defect as the audit tab rendering for a MEMBER: a control
// that appears usable and answers with a refusal. Server-declared here, the
// way testable already is, so the client needs no second copy of the rule.
func TestCredentialRead_ExposesSensitivity(t *testing.T) {
	t.Parallel()
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	const credID = "cred-sealed"
	seedCredentialEnc(t, db, wsID, userID, credID, "PROD_DB", "hunter2")
	if _, err := db.Exec(`UPDATE credentials SET sensitivity = 'SEALED' WHERE id = ?`, credID); err != nil {
		t.Fatalf("seal: %v", err)
	}

	getReq := httptest.NewRequest("GET", "/api/v1/credentials/"+credID, nil)
	getReq.SetPathValue("credentialId", credID)
	getReq = getReq.WithContext(withWorkspace(getReq.Context(), wsID, "OWNER"))
	getRR := httptest.NewRecorder()
	h.Get(getRR, getReq)
	var got struct {
		Sensitivity string `json:"sensitivity"`
	}
	if err := json.Unmarshal(getRR.Body.Bytes(), &got); err != nil {
		t.Fatalf("Get unmarshal: %v", err)
	}
	if got.Sensitivity != "SEALED" {
		t.Errorf("Get sensitivity = %q, want SEALED — without it no client can tell that "+
			"reveal is impossible before trying", got.Sensitivity)
	}

	listReq := httptest.NewRequest("GET", "/api/v1/credentials", nil)
	listReq = listReq.WithContext(withWorkspace(listReq.Context(), wsID, "OWNER"))
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)
	var list []struct {
		ID          string `json:"id"`
		Sensitivity string `json:"sensitivity"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
		t.Fatalf("List unmarshal: %v", err)
	}
	for _, c := range list {
		if c.ID == credID && c.Sensitivity != "SEALED" {
			t.Errorf("List sensitivity = %q, want SEALED — List and Get scan into "+
				"credentialResponse at two independent sites and must agree", c.Sensitivity)
		}
	}
}

// probeArmSourceFile is the file whose `switch provider` the test below derives
// from. Named so the failure message can say which file went missing rather than
// reporting an unhelpful parse error against a path nobody recognises.
const probeArmSourceFile = "credentials_test_endpoint.go"

// TestProbeArms_AreAllRegistered closes the direction this file's doc comment
// has always claimed and never actually had.
//
// TestProbeSupported_ProbelessRegistrationFails catches a registration with no
// probe. The opposite mistake — adding a `case "COHERE"` arm to
// probeProviderInner and forgetting probeSupportedProviders — is still silent,
// and it is silent in the direction that costs a user something real: the server
// CAN validate the key, the wizard hides the Test button because the map says
// otherwise, and the operator is left to find out at run time.
//
// Calling the function cannot detect it: an unregistered arm and an absent arm
// are indistinguishable from the outside. So the switch itself is read. Parsing
// our own source is unusual in this package and deliberate — the alternative is
// a third hand-maintained list of provider names, which is precisely the drift
// this file exists to prevent.
func TestProbeArms_AreAllRegistered(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, probeArmSourceFile, nil, 0)
	if err != nil {
		// t.Fatal, never t.Skip. A skip reports the same "ok" as a pass, so a
		// renamed file would retire this coverage without anyone noticing.
		t.Fatalf("parse %s (the file this test derives the probe arms from): %v", probeArmSourceFile, err)
	}

	arms := probeSwitchCases(t, parsed)
	if len(arms) == 0 {
		t.Fatalf("found no case labels on probeProviderInner's switch in %s — the switch was "+
			"renamed or restructured and this test is no longer reading anything", probeArmSourceFile)
	}

	for _, provider := range arms {
		if _, ok := probeSupportedProviders[provider]; !ok {
			t.Errorf("probeProviderInner has a real probe arm for %q but probeSupportedProviders "+
				"does not list it, so the Test button stays hidden for a provider the server "+
				"can validate", provider)
		}
	}
}

// probeSwitchCases returns the string case labels of the top-level
// `switch provider` inside probeProviderInner. The Tag check is what keeps the
// inner `switch resp.StatusCode` switches — which carry http.Status* idents, not
// string literals — from being read as provider names.
func probeSwitchCases(t *testing.T, parsed *ast.File) []string {
	t.Helper()

	var fn *ast.FuncDecl
	for _, decl := range parsed.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "probeProviderInner" {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatalf("probeProviderInner is not declared in %s — it was renamed or moved, and this "+
			"test must move with it", probeArmSourceFile)
	}

	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		tag, ok := sw.Tag.(*ast.Ident)
		if !ok || tag.Name != "provider" {
			return true
		}
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range clause.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("case label %s is not an unquotable string literal: %v", lit.Value, err)
				}
				out = append(out, name)
			}
		}
		return false
	})
	return out
}
