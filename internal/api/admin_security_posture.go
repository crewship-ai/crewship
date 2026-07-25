package api

// Instance security posture (#1379).
//
// Instance-level security is entirely env-driven and, by design, NOT flippable
// from the app — these are deploy decisions, not workspace settings. But their
// *state* was invisible: "are we storing credentials in plaintext? is signup
// open? is the rate limiter off?" had no answer short of SSHing to the box and
// reading the process environment. That is a bad place to keep a security
// posture, because the person who needs it (an admin triaging an incident) is
// often exactly the person who can't get a shell.
//
// This endpoint answers those questions and nothing else. It reports booleans
// and enum-ish state ONLY — never a secret, never a key, never a client secret,
// not even a redacted one. "Configured / not configured" is the whole contract,
// so there is no value here worth stealing and no reason to gate it beyond the
// admin role the rest of the admin surface already uses.
//
// Every field reads the SAME accessor the runtime enforces with, rather than
// re-parsing the environment locally. A posture that parses a flag its own way
// eventually disagrees with the code that acts on it, and a security report
// that confidently states the wrong thing is worse than no report.

import (
	"net/http"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// SecurityPostureHandler serves the read-only instance posture.
type SecurityPostureHandler struct {
	// allowSignup mirrors cfg.Auth.AllowSignup, threaded in at construction
	// because config isn't reachable from the handler layer.
	allowSignup bool
	// oauthConfigured mirrors "cfg.Auth.GoogleClientID and GoogleSecret are
	// both set" — presence only; neither value is stored here.
	oauthConfigured bool
}

// NewSecurityPostureHandler builds the handler from the resolved config values
// the server already holds. Booleans are taken at construction so the handler
// never touches the config struct (and therefore can never accidentally
// serialize a secret out of it).
func NewSecurityPostureHandler(allowSignup, oauthConfigured bool) *SecurityPostureHandler {
	return &SecurityPostureHandler{allowSignup: allowSignup, oauthConfigured: oauthConfigured}
}

// postureWarning is a derived, human-readable risk note. The raw booleans say
// what is set; a warning says why it matters and what it costs — which is the
// part an operator actually needs at 2am.
type postureWarning struct {
	// Key is stable and machine-matchable so a script can gate on it.
	Key      string `json:"key"`
	Severity string `json:"severity"` // "high" | "medium" | "info"
	Message  string `json:"message"`
}

type securityPostureResponse struct {
	// Environment is CREWSHIP_ENV as the process sees it ("prod",
	// "production", "dev", …, or "" when unset). Several guards key off it,
	// so an operator debugging "why is the limiter still on?" needs to see
	// what the process actually thinks it is.
	Environment string `json:"environment"`

	// Encryption — the highest-signal pair on this page.
	EncryptionKeyConfigured bool `json:"encryption_key_configured"`
	PlaintextSecretsAllowed bool `json:"plaintext_secrets_allowed"`

	// Egress ceiling. This is the instance half of the two-key private-egress
	// opt-in; the crew half is per-crew allow_private_endpoints.
	PrivateEndpointsCeiling bool `json:"private_endpoints_ceiling"`

	// Auth surface.
	SignupOpen      bool `json:"signup_open"`
	OAuthConfigured bool `json:"oauth_configured"`
	EmailConfigured bool `json:"email_configured"`

	// Rate limiting. Disabled is what the operator asked for;
	// EffectivelyDisabled is what they got — in prod the limiter runs
	// regardless, and conflating the two hides a real misconfiguration.
	RateLimitDisabled            bool `json:"rate_limit_disabled"`
	RateLimitEffectivelyDisabled bool `json:"rate_limit_effectively_disabled"`

	Warnings []postureWarning `json:"warnings"`
}

// isProdEnv mirrors the production test the rate-limit guard uses, so the
// posture's notion of "prod" cannot drift from the one that keeps the limiter
// on.
func isProdEnv(env string) bool {
	e := strings.ToLower(strings.TrimSpace(env))
	return e == "prod" || e == "production"
}

// buildSecurityPosture assembles the report. Split out from the HTTP handler so
// the derivation (especially the warning rules) is testable without a request.
//
// rateLimitOff is passed in rather than read from the package-level
// rateLimitDisabled: that var is resolved once at init from the environment, so
// a test could never exercise the prod-override branch — and a branch no test
// can reach is one nobody notices breaking.
func buildSecurityPosture(allowSignup, oauthConfigured, emailConfigured, rateLimitOff bool) securityPostureResponse {
	env := os.Getenv("CREWSHIP_ENV")
	prod := isProdEnv(env)

	p := securityPostureResponse{
		Environment:             env,
		EncryptionKeyConfigured: encryption.KeyConfigured(),
		PlaintextSecretsAllowed: encryption.PlaintextSecretsAllowed(),
		PrivateEndpointsCeiling: orchestrator.InstanceAllowsPrivateEndpoints(),
		SignupOpen:              allowSignup,
		OAuthConfigured:         oauthConfigured,
		EmailConfigured:         emailConfigured,
		RateLimitDisabled:       rateLimitOff,
		// In prod the limiter runs no matter what the flag says
		// (MustNotDisableRateLimitInProd), so the *effective* state is what an
		// operator should act on.
		RateLimitEffectivelyDisabled: rateLimitOff && !prod,
		Warnings:                     []postureWarning{},
	}

	// --- Derived warnings, worst first ----------------------------------
	//
	// Each one names the consequence, not just the flag. "plaintext_secrets is
	// on" is a fact; "every credential in this instance is readable by anyone
	// with the DB file" is the thing that gets acted on.

	if p.PlaintextSecretsAllowed {
		sev := "high"
		msg := "Credentials may be stored UNENCRYPTED at rest: " +
			encryption.AllowPlaintextSecretsEnvVar + " is set. Anyone with the database file can read every stored secret."
		if prod {
			msg = "PRODUCTION instance is running with " + encryption.AllowPlaintextSecretsEnvVar +
				" set — credentials may be stored unencrypted at rest and are readable by anyone with the database file."
		}
		p.Warnings = append(p.Warnings, postureWarning{Key: "plaintext_secrets_allowed", Severity: sev, Message: msg})
	}

	if !p.EncryptionKeyConfigured {
		// Distinct from the flag above: no key means secret WRITES fail closed
		// unless the plaintext opt-out is set. Together they are the
		// silently-storing-plaintext combination.
		sev := "medium"
		msg := "No ENCRYPTION_KEY is configured. Secret writes fail closed; set one (openssl rand -hex 32) to store credentials encrypted."
		if p.PlaintextSecretsAllowed {
			sev = "high"
			msg = "No ENCRYPTION_KEY is configured AND plaintext secrets are explicitly allowed — new credentials are being written in the clear."
		}
		p.Warnings = append(p.Warnings, postureWarning{Key: "encryption_key_missing", Severity: sev, Message: msg})
	}

	if p.RateLimitEffectivelyDisabled {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "rate_limit_disabled", Severity: "medium",
			Message: "The API rate limiter is OFF. Credential-stuffing and the /credentials/test validation oracle are unthrottled.",
		})
	} else if p.RateLimitDisabled && prod {
		// Not a vulnerability — the guard held — but it means someone's intent
		// and the running state disagree, which is worth knowing before the
		// next deploy moves this config somewhere the guard doesn't apply.
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "rate_limit_disabled_ignored_in_prod", Severity: "info",
			Message: "A rate-limit disable flag is set but IGNORED because this is a production environment. The limiter is running.",
		})
	}

	if p.SignupOpen {
		sev := "medium"
		if prod {
			sev = "high"
		}
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "signup_open", Severity: sev,
			Message: "Open signup is enabled — anyone who can reach this instance can create an account.",
		})
	}

	if p.PrivateEndpointsCeiling {
		p.Warnings = append(p.Warnings, postureWarning{
			Key: "private_endpoints_ceiling_open", Severity: "info",
			Message: "The instance ceiling for private-network egress is OPEN. Crews that also set allow_private_endpoints can reach RFC1918/loopback addresses. Link-local and cloud-metadata stay blocked regardless.",
		})
	}

	return p
}

// Get handles GET /api/v1/admin/security-posture.
//
// Admin-gated like the rest of the admin surface. The response deliberately
// contains no secret material at all — only whether things are configured — so
// the gate is about who should be reasoning about instance posture, not about
// protecting a payload.
func (h *SecurityPostureHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	// Read the mailer's own answer rather than re-testing RESEND_* here — it
	// is the transport that knows whether it can actually send.
	emailConfigured := mailer.NewFromEnv().Configured()
	writeJSON(w, http.StatusOK, buildSecurityPosture(h.allowSignup, h.oauthConfigured, emailConfigured, rateLimitDisabled))
}
