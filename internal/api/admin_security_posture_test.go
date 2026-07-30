package api

// #1379 — the instance security-posture report.
//
// Two things are load-bearing here. First, the report must never carry a secret
// value: the whole reason it can be a plain admin GET is that "configured /
// not configured" is all it says. Second, the warnings have to fire on the
// combinations that actually matter, because the raw booleans are what an
// operator would already have had to go read the environment for.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func postureFor(t *testing.T, env map[string]string, allowSignup, oauth, email bool) securityPostureResponse {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	return buildSecurityPosture(allowSignup, oauth, email, false, postureState{BackupsRecorded: 1})
}

func warningKeys(p securityPostureResponse) map[string]string {
	out := map[string]string{}
	for _, w := range p.Warnings {
		out[w.Key] = w.Severity
	}
	return out
}

func TestSecurityPosture_NeverLeaksSecretValues(t *testing.T) {
	// The guarantee that lets this be a simple admin GET. Seed every secret-ish
	// env var with a recognisable sentinel and assert none of them reach the
	// wire — including via a warning message, which is the easy place to leak
	// one while trying to be helpful.
	const sentinel = "SUPERSECRETVALUE12345"
	t.Setenv("ENCRYPTION_KEY", sentinel)
	t.Setenv("RESEND_API_KEY", sentinel)
	t.Setenv("RESEND_FROM", "noreply@example.com")
	t.Setenv("JWT_SECRET", sentinel)
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "true")
	t.Setenv("CREWSHIP_ENV", "prod")

	p := buildSecurityPosture(true, true, true, true, postureState{BackupsRecorded: 1})
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), sentinel) {
		t.Fatalf("secret value leaked into the posture response:\n%s", blob)
	}
	// Sanity: the report is not empty — a response that says nothing would
	// trivially pass the assertion above.
	if len(p.Warnings) == 0 {
		t.Error("expected warnings for a prod instance with plaintext secrets allowed")
	}
}

func TestSecurityPosture_PlaintextSecretsIsHigh(t *testing.T) {
	p := postureFor(t, map[string]string{
		encryption.AllowPlaintextSecretsEnvVar: "true",
		"ENCRYPTION_KEY":                       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}, false, false, false)

	if !p.PlaintextSecretsAllowed {
		t.Fatal("flag not reflected")
	}
	if got := warningKeys(p)["plaintext_secrets_allowed"]; got != "high" {
		t.Errorf("severity = %q, want high", got)
	}
}

func TestSecurityPosture_NoKeyPlusPlaintextEscalates(t *testing.T) {
	// Either alone is bad; together they mean credentials are actively being
	// written in the clear, and the message should say so rather than
	// repeating the generic "set a key" advice.
	p := postureFor(t, map[string]string{
		encryption.AllowPlaintextSecretsEnvVar: "true",
		"ENCRYPTION_KEY":                       "",
	}, false, false, false)

	w := warningKeys(p)
	if w["encryption_key_missing"] != "high" {
		t.Errorf("missing-key severity = %q, want high when plaintext is also allowed", w["encryption_key_missing"])
	}
	var msg string
	for _, x := range p.Warnings {
		if x.Key == "encryption_key_missing" {
			msg = x.Message
		}
	}
	if !strings.Contains(msg, "in the clear") {
		t.Errorf("combined message should name the consequence, got %q", msg)
	}
}

func TestSecurityPosture_RateLimitDisabledInProdIsIgnoredNotDisabled(t *testing.T) {
	// The guard keeps the limiter on in prod. Reporting "disabled" there would
	// send an operator chasing an exposure that doesn't exist — but silently
	// reporting "enabled" would hide that their config says otherwise.
	t.Setenv("CREWSHIP_ENV", "production")
	p := buildSecurityPosture(false, false, false, true /* operator asked for it off */, postureState{BackupsRecorded: 1})

	if !p.RateLimitDisabled {
		t.Error("the operator's flag should still be reported as set")
	}
	if p.RateLimitEffectivelyDisabled {
		t.Error("prod must report the limiter as effectively ON")
	}
	if got := warningKeys(p)["rate_limit_disabled_ignored_in_prod"]; got != "info" {
		t.Errorf("want an info-level 'ignored in prod' note, got %q", got)
	}
}

func TestSecurityPosture_SignupSeverityRisesInProd(t *testing.T) {
	dev := postureFor(t, map[string]string{"CREWSHIP_ENV": "dev"}, true, false, false)
	if got := warningKeys(dev)["signup_open"]; got != "medium" {
		t.Errorf("dev signup severity = %q, want medium", got)
	}

	prod := postureFor(t, map[string]string{"CREWSHIP_ENV": "prod"}, true, false, false)
	if got := warningKeys(prod)["signup_open"]; got != "high" {
		t.Errorf("prod signup severity = %q, want high", got)
	}
}

func TestSecurityPosture_PrivateEndpointCeilingReflectsTheEnforcedAccessor(t *testing.T) {
	// Reads orchestrator.InstanceAllowsPrivateEndpoints — the same accessor
	// that gates traffic — so the report cannot claim a state the runtime
	// doesn't have. "on" is a valid truthy spelling there but not in a naive
	// strconv.ParseBool, which is exactly the drift this guards.
	on := postureFor(t, map[string]string{"CREWSHIP_ALLOW_PRIVATE_ENDPOINTS": "on"}, false, false, false)
	if !on.PrivateEndpointsCeiling {
		t.Error(`"on" must be honoured — it is truthy for the enforcing accessor`)
	}
	if got := warningKeys(on)["private_endpoints_ceiling_open"]; got != "info" {
		t.Errorf("want an info note when the ceiling is open, got %q", got)
	}

	off := postureFor(t, map[string]string{"CREWSHIP_ALLOW_PRIVATE_ENDPOINTS": ""}, false, false, false)
	if off.PrivateEndpointsCeiling {
		t.Error("unset ceiling must report closed")
	}
}

func TestSecurityPosture_CleanInstanceHasNoHighWarnings(t *testing.T) {
	p := postureFor(t, map[string]string{
		encryption.AllowPlaintextSecretsEnvVar: "",
		"ENCRYPTION_KEY":                       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"CREWSHIP_ALLOW_PRIVATE_ENDPOINTS":     "",
		"CREWSHIP_ENV":                         "prod",
	}, false, true, true)

	for _, w := range p.Warnings {
		if w.Severity == "high" {
			t.Errorf("a hardened instance should raise no high warnings; got %+v", w)
		}
	}
	if !p.EncryptionKeyConfigured || p.PlaintextSecretsAllowed || p.SignupOpen {
		t.Errorf("hardened posture misreported: %+v", p)
	}
}

func TestSecurityPosture_RequiresAdmin(t *testing.T) {
	h := NewSecurityPostureHandler(false, false, nil, nil)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/security-posture", nil), "u1", "ws1", "MEMBER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a MEMBER", rr.Code)
	}
}

func TestSecurityPosture_AdminGetsTheReport(t *testing.T) {
	h := NewSecurityPostureHandler(false, false, nil, nil)
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/admin/security-posture", nil), "u1", "ws1", "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var p securityPostureResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Warnings == nil {
		t.Error("warnings must serialize as [] rather than null")
	}
}

// TestSecurityPosture_WarningKeysAreTheDocumentedSet pins the warning-key set
// as a contract. CodeRabbit caught the docs listing 5 of the 6 emitted keys on
// this PR; enumerating them here means the next key added has to come past this
// test, which is the prompt to update docs/cli/admin.mdx with it.
//
// Deliberately not reading the .mdx from Go — coupling a unit test to a docs
// file path is more fragile than it is worth. This asserts the source of truth;
// the doc list is checked against it by eye at review time.
func TestSecurityPosture_WarningKeysAreTheDocumentedSet(t *testing.T) {
	documented := map[string]bool{
		// Environment-derived.
		"plaintext_secrets_allowed":           true,
		"encryption_key_missing":              true,
		"rate_limit_disabled":                 true,
		"rate_limit_disabled_ignored_in_prod": true,
		"signup_open":                         true,
		"private_endpoints_ceiling_open":      true,
		// State-derived: what the instance became, not how it started.
		"encryption_key_generated":       true,
		"privileged_credentials_enabled": true,
		"private_endpoints_in_use":       true,
		"seed_account_default_password":  true,
		"no_backup_recorded":             true,
	}

	// Drive every branch that can emit a warning and collect the keys.
	seen := map[string]bool{}
	collect := func(p securityPostureResponse) {
		for _, w := range p.Warnings {
			seen[w.Key] = true
		}
	}
	// Worst case: no key, plaintext allowed, signup open, ceiling open, dev.
	t.Setenv("CREWSHIP_ENV", "dev")
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "true")
	t.Setenv("CREWSHIP_ALLOW_PRIVATE_ENDPOINTS", "1")
	collect(buildSecurityPosture(true, false, false, true, postureState{BackupsRecorded: 1, BackupsRecordedKnown: true}))
	// Prod with the limiter flag set — the only path to the ignored-in-prod key.
	t.Setenv("CREWSHIP_ENV", "prod")
	collect(buildSecurityPosture(true, false, false, true, postureState{BackupsRecorded: 1, BackupsRecordedKnown: true}))
	// The state-derived half. These emit from postureState alone, so the
	// env-driven cases above can never reach them — before this, five keys
	// sat in the emitted set with no case that produced them and the
	// contract passed by never looking.
	collect(buildSecurityPosture(false, false, false, false, postureState{
		EncryptionKeySource:            "generated",
		PrivilegedCredentialWorkspaces: 2,
		PrivateEndpointCrews:           3,
		SeedAccountDefaultPassword:     true,
		BackupsRecorded:                0,
		BackupsRecordedKnown:           true,
	}))

	for k := range seen {
		if !documented[k] {
			t.Errorf("warning key %q is emitted but not in the documented set — add it to docs/cli/admin.mdx and to this test", k)
		}
	}
	for k := range documented {
		if !seen[k] {
			t.Errorf("warning key %q is documented but no branch emitted it — stale docs, or a branch this test no longer reaches", k)
		}
	}
}
